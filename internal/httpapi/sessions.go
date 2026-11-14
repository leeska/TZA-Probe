package httpapi

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// sessionsFile keeps the browser sessions across restarts, next to the
// account in the data directory. It holds only hashes of the session tokens
// (as the map does), their expiry and the account hash they were issued for,
// so reading it does not give a usable cookie.
const sessionsFile = "admin-sessions"

func (a *API) sessionsPath() string {
	if a.AccountDir == "" {
		return ""
	}
	return filepath.Join(a.AccountDir, sessionsFile)
}

// loadSessions fills a.sessions from the file, dropping expired ones.
func (a *API) loadSessions() {
	path := a.sessionsPath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("admin sessions not restored", "error", err)
		}
		return
	}
	var saved map[string]browserSession
	if err := json.Unmarshal(raw, &saved); err != nil {
		slog.Warn("admin sessions file unreadable; starting signed out", "error", err)
		return
	}
	now := time.Now()
	for key, s := range saved {
		if now.Before(s.Expires) && len(a.sessions) < maxSessions {
			a.sessions[key] = s
		}
	}
}

// saveSessionsLocked writes a.sessions; the caller holds a.mu. A failed write
// only costs the sessions at the next restart.
func (a *API) saveSessionsLocked() {
	path := a.sessionsPath()
	if path == "" {
		return
	}
	raw, _ := json.Marshal(a.sessions)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err == nil {
		err = os.Rename(tmp, path)
		if err != nil {
			slog.Warn("admin sessions not saved", "error", err)
		}
	} else {
		slog.Warn("admin sessions not saved", "error", err)
	}
}
