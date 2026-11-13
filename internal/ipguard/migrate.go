package ipguard

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leeska/TZA-Probe/internal/storage"
)

// LegacyFile is where v2.0.0 kept the region drift tables, next to the main
// database. Migration 1 imports it and renames it to LegacyFile+".migrated".
const LegacyFile = "ipguard.db"

// Migrations moves region drift into the main database: version 1 creates the
// tables and copies policies and jobs out of ipguard.db when that file exists.
func Migrations() []storage.Migration {
	return []storage.Migration{{Module: "regiondrift", Version: 1, Name: "tables in the main database, import ipguard.db", Up: func(c *storage.MigrationContext) error {
		if _, err := c.Tx.Exec(ddl); err != nil {
			return err
		}
		legacy := filepath.Join(c.DataDir, LegacyFile)
		if _, err := os.Stat(legacy); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if err := importLegacy(c.Tx, legacy); err != nil {
			return fmt.Errorf("import %s: %w", legacy, err)
		}
		c.AfterCommit(func() error {
			// The rows are committed in the main database; keep the old file
			// (and any WAL left beside it) under a new name instead of deleting it.
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := os.Rename(legacy+suffix, legacy+".migrated"+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			return nil
		})
		return nil
	}}, {Module: "regiondrift", Version: 2, Name: "tasks across nodes, per-node settings", Up: func(c *storage.MigrationContext) error {
		if _, err := c.Tx.Exec(tasksDDL); err != nil {
			return err
		}
		return importPolicies(c.Tx)
	}}}
}

// importLegacy copies every row of the old file into tx. The old file is
// opened read-write so SQLite replays a WAL a crashed v2.0.0 left behind.
func importLegacy(tx *sql.Tx, path string) error {
	old, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer old.Close()
	copyRows := func(from, to string, columns int) (int, error) {
		var exists int
		if err := old.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", from).Scan(&exists); err != nil || exists == 0 {
			return 0, err
		}
		rows, err := old.Query("SELECT * FROM " + from)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		names, err := rows.Columns()
		if err != nil {
			return 0, err
		}
		if len(names) != columns {
			return 0, fmt.Errorf("%s has %d columns, want %d", from, len(names), columns)
		}
		marks := "?"
		for i := 1; i < columns; i++ {
			marks += ",?"
		}
		n := 0
		for rows.Next() {
			values := make([]any, columns)
			pointers := make([]any, columns)
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				return n, err
			}
			if _, err := tx.Exec("INSERT INTO "+to+" VALUES("+marks+")", values...); err != nil {
				return n, err
			}
			n++
		}
		return n, rows.Err()
	}
	if _, err := copyRows("policies", "ipguard_policies", 3); err != nil {
		return err
	}
	_, err = copyRows("jobs", "ipguard_jobs", 12)
	return err
}
