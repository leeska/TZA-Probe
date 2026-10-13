package model

import "errors"

type Job struct {
	ID   string `json:"id"`
	Task Probe  `json:"task"`
}

type Hop struct {
	TTL     int     `json:"ttl"`
	Address string  `json:"address"`
	ASN     string  `json:"asn"`
	RTT     float64 `json:"rtt"`
}

type Result struct {
	Reason   string   `json:"reason,omitempty"`
	JobID    string   `json:"job_id"`
	Status   string   `json:"status"`
	Latency  *float64 `json:"latency"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	Route    string   `json:"route"`
	Hops     []Hop    `json:"hops"`
	// Unlocks is the per-service verdict of an unlock job: what this node's own
	// egress can reach, which region each service answered for, and why not.
	Unlocks []UnlockResult `json:"unlocks,omitempty"`
}

func (r Result) Validate() error {
	switch r.Reason {
	case "", "dns", "permission", "tool_missing", "tool_failed", "timeout", "connect_failed", "synthetic_dns":
	default:
		return errors.New("invalid result reason")
	}

	if r.Status != "ok" && r.Status != "timeout" && r.Status != "unavailable" && r.Status != "failed" {
		return errors.New("invalid result status")
	}
	// A latency probe proves itself with a response and a round trip; an unlock
	// job has neither, it proves itself with the verdicts it carries.
	if r.Status == "ok" && len(r.Unlocks) == 0 && (r.Received == 0 || r.Latency == nil) {
		return errors.New("successful result requires a response and latency")
	}
	if len(r.Unlocks) > len(UnlockCatalog) {
		return errors.New("invalid unlock result")
	}
	for _, u := range r.Unlocks {
		if !KnownUnlockService(u.Service) || !ValidUnlockStatus(u.Status) || len(u.Region) > 40 || len(u.Detail) > 80 {
			return errors.New("invalid unlock result")
		}
	}
	if r.Sent < 0 || r.Sent > 5 || r.Received < 0 || r.Received > r.Sent || len(r.Hops) > 30 || len(r.Route) > 160 {
		return errors.New("invalid result")
	}
	if r.Latency != nil && (!finite(*r.Latency) || *r.Latency < 0 || *r.Latency > 60000) {
		return errors.New("invalid latency")
	}
	for _, h := range r.Hops {
		if h.TTL < 1 || h.TTL > 30 || len(h.Address) > 100 || len(h.ASN) > 20 || !finite(h.RTT) || h.RTT < 0 || h.RTT > 60000 {
			return errors.New("invalid hop")
		}
	}
	return nil
}

type Observation struct {
	Task   Probe  `json:"task"`
	At     int64  `json:"at"`
	Result Result `json:"result"`
	Stale  bool   `json:"stale"`
}

// Redact returns the observation as the public contract exposes it: hop
// addresses keep only their masked prefix, and a stale or
// failed probe reports no route instead of the label of an older, successful run.
// The hop slice is masked in place, which is what the shared caller already owned.
func (o Observation) Redact() Observation {
	for i := range o.Result.Hops {
		hop := &o.Result.Hops[i]
		hop.Address = PublicAddress(hop.Address)
		hop.ASN = NormalizeASN(hop.ASN)
	}
	if o.Stale || o.Result.Status != "ok" {
		o.Result.Route = "未知"
	}
	return o
}

type NetworkSample struct {
	At      int64    `json:"at"`
	Latency *float64 `json:"latency"`
	Failure *float64 `json:"failure"`
}

type NetworkSeries struct {
	Name    string          `json:"name"`
	Family  string          `json:"family"`
	Host    string          `json:"host"`
	Port    int             `json:"port"`
	Region  string          `json:"region"`
	Carrier string          `json:"carrier"`
	Samples []NetworkSample `json:"samples"`
}

// LatencySample is one finished TCP probe. The public card draws each probe as
// its own cell, so the cell colours describe individual measurements instead of
// a single aggregated gauge. Latency is nil when that probe failed.
type LatencySample struct {
	NodeID   string   `json:"node_id"`
	Carrier  string   `json:"carrier"`
	Region   string   `json:"region"`
	Family   string   `json:"family"`
	At       int64    `json:"at"`
	Latency  *float64 `json:"latency"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
}
