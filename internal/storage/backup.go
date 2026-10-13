package storage

import (
	"context"
	"os"
	"path/filepath"
)

// Backup writes a consistent snapshot of the whole database with VACUUM INTO
// and returns its path plus a cleanup that removes it. It is a read
// transaction on the live database, so the panel keeps running and Agents keep
// reporting while it is taken. The snapshot goes into a fresh directory beside
// the database rather than the system temp dir: same filesystem, and the
// database can be larger than a tmpfs.
func (s *Store) Backup(ctx context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Dir(s.path), ".backup-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	path := filepath.Join(dir, "tza.db")
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
