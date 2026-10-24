package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestAgentStateRejectsDelayedHeartbeatsAcrossRecoveryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Second)
	session := model.Secret()
	m := metrics()
	m.AgentState = model.AgentState{Session: session, Sequence: 10}
	if err := reportSession(s, ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	failure := model.AgentStatus{AgentState: model.AgentState{Session: session, Sequence: 9}, State: "collect_failed", Reason: "disk"}
	assertFresh := func(at time.Time) {
		t.Helper()
		views, err := s.Nodes(ctx, at)
		if err != nil || views[0].MetricsStale || views[0].Metrics == nil || views[0].AgentStatus != nil {
			t.Fatalf("old fault overwrote recovery: %+v %v", views, err)
		}
	}
	// The fault was sent before recovery but arrives afterwards.
	if err := s.Heartbeat(ctx, id, failure, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	assertFresh(now.Add(time.Second))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(ctx, id, failure, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertFresh(now.Add(2 * time.Second))
	// A genuine new failure, including repeated heartbeats, still updates contact.
	failure.Sequence = 11
	for _, seconds := range []int{5, 10} {
		if err := s.Heartbeat(ctx, id, failure, now.Add(time.Duration(seconds)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.Nodes(ctx, now.Add(10*time.Second))
	if err != nil || !views[0].MetricsStale || views[0].LastSeen != now.Unix()+10 {
		t.Fatalf("new fault lost: %+v %v", views, err)
	}
	// A restarted Agent may begin with a smaller sequence and a new random session.
	m.AgentState = model.AgentState{Session: model.Secret(), Sequence: 1}
	if err := reportSession(s, ctx, id, m, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertFresh(now.Add(15 * time.Second))
	failure.Sequence = 999
	if err := s.Heartbeat(ctx, id, failure, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertFresh(now.Add(20 * time.Second))
	// Even a delayed old-process metric report cannot reclaim the node or bill traffic.
	old := metrics()
	old.AgentState = failure.AgentState
	old.NetIn += 9999
	if err := s.Report(ctx, id, old, now.Add(25*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertFresh(now.Add(25 * time.Second))
	views, _ = s.Nodes(ctx, now.Add(25*time.Second))
	if views[0].TrafficUsed != 0 {
		t.Fatal("retired report billed traffic")
	}
	if err := s.Clean(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := s.db.QueryRow("SELECT count(*) FROM node_agent_sessions WHERE node_id=?", id).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("session retention: %d %v", sessions, err)
	}
	if err := s.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM node_agent_sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("orphan session", err)
	}
}

// Complete the same challenge round trip as the HTTP transport.
func reportSession(s *Store, ctx context.Context, id string, m model.Metrics, now time.Time) error {
	err := s.Report(ctx, id, m, now)
	var challenge *model.AgentSessionChallenge
	if errors.As(err, &challenge) {
		m.Proof = challenge.Challenge
		return s.Report(ctx, id, m, now)
	}
	return err
}

func TestUnknownOldSessionCannotRetireHealthyProcess(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Second)
	old := metrics()
	old.AgentState = model.AgentState{Session: model.Secret(), Sequence: 2}
	// An old process may have received a challenge just before it exited.
	var initial *model.AgentSessionChallenge
	if err := s.Report(ctx, id, old, now); !errors.As(err, &initial) {
		t.Fatalf("first request must require confirmation: %v", err)
	}
	old.Proof = initial.Challenge
	current := metrics()
	current.AgentState = model.AgentState{Session: model.Secret(), Sequence: 3}
	if err := reportSession(s, ctx, id, current, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Neither an unseen delayed packet nor its stale confirmation may take over.
	for _, proof := range []string{"", initial.Challenge} {
		old.Proof = proof
		var challenge *model.AgentSessionChallenge
		if err := s.Report(ctx, id, old, now.Add(2*time.Second)); !errors.As(err, &challenge) {
			t.Fatalf("old packet took ownership: %v", err)
		}
		if challenge.Challenge == initial.Challenge {
			t.Fatal("challenge not bound to current owner")
		}
	}
	failure := model.AgentStatus{AgentState: old.AgentState, State: "collect_failed", Reason: "disk"}
	var challenge *model.AgentSessionChallenge
	if err := s.Heartbeat(ctx, id, failure, now.Add(3*time.Second)); !errors.As(err, &challenge) {
		t.Fatal(err)
	}
	current.Sequence = 6
	current.NetIn += 123
	if err := s.Report(ctx, id, current, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err := s.Nodes(ctx, now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	v := views[0]
	if v.MetricsAt != now.Add(6*time.Second).Unix() || v.MetricsStale || v.AgentStatus != nil || v.TrafficUsed != 123 || v.Metrics.Proof != "" {
		t.Fatalf("healthy process or traffic overwritten: %+v", v)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM node_agent_sessions WHERE node_id=?", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unconfirmed requests changed sessions: %d %v", count, err)
	}
}
