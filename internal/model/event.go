package model

import "time"

// EventDeliveryWindow is how long an alert stays worth sending. Events pile up
// while no channel is configured; setting one up later must not replay weeks
// of offline and recovery notices, so older events are left undelivered.
const EventDeliveryWindow = 24 * time.Hour

type Event struct {
	LastError string `json:"last_error"`
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Active    bool   `json:"active"`
	At        int64  `json:"at"`
	Delivered bool   `json:"delivered"`
	Attempts  int    `json:"attempts"`
}

// EventQuery pages the event log: newest first, ids below Before (0 = from the
// newest), optionally one node or one kind. Active keeps only alerts that are
// firing right now, each as the event that last reported it.
type EventQuery struct {
	Before int64
	Limit  int
	NodeID string
	Kind   string
	Active bool
}

const (
	DefaultEventPage = 100
	MaxEventPage     = 500
)

// Normalize clamps the page size so a caller cannot ask for the whole log.
func (q EventQuery) Normalize() EventQuery {
	if q.Limit <= 0 {
		q.Limit = DefaultEventPage
	}
	q.Limit = min(q.Limit, MaxEventPage)
	q.Before = max(q.Before, 0)
	return q
}
