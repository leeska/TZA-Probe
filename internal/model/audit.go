package model

// AuditEntry is one privileged action in the audit log: who did it (Actor:
// the panel session's username, "api-key" for the admin token), from where
// (Source: the client address as the Server saw it, plus any forwarded-for
// header), what (Action, e.g. "shell.exec" or "PUT /api/v1/admin/nodes/{id}")
// and the outcome. Detail carries the action's own words, such as the command
// that ran; it never carries request bodies, which may hold secrets.
type AuditEntry struct {
	ID     int64  `json:"id"`
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Source string `json:"source"`
	Action string `json:"action"`
	NodeID string `json:"node_id,omitempty"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// AuditRetentionDays is how long audit entries are kept.
const AuditRetentionDays = 365
