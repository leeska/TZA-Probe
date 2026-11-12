package storage

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const schemaVersion = 30

//go:embed schema.sql
var schema string

func initializeDatabase(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch {
	case version == schemaVersion:
		return tx.Commit()
	case version > schemaVersion:
		return errors.New("database version is newer than this binary")
	case version != 0:
		return fmt.Errorf("database schema %d is unsupported; use a new data directory for schema %d", version, schemaVersion)
	}
	var objects int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return err
	}
	if objects != 0 {
		return errors.New("database is not empty; use a new data directory")
	}
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func probeKeyExpr(prefix string) string {
	field := func(key string) string {
		return "COALESCE(json_extract(" + prefix + "task,'$." + key + "'),'')"
	}
	return "COALESCE(" + prefix + `task_id,'')||'|'||` + field("kind") + "||'|'||" + field("family") + "||'|'||" +
		field("host") + "||'|'||" + field("port") + "||'|'||" + field("region") + "||'|'||" + field("carrier")
}

// CheckDataDir reports, without creating or changing anything, whether this
// binary can run on the data directory dir: no database yet (a fresh
// install), or one at this schema. The installer runs it with the new binary
// before replacing the old one, so an upgrade that would refuse to start
// stops while the running server is still untouched.
func CheckDataDir(dir string) error {
	current := filepath.Join(dir, "tza-probe.db")
	if _, err := os.Stat(current); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(dir, "tza-v2.db")); err == nil {
			return fmt.Errorf("%s holds an older database (tza-v2.db); schema %d needs a new data directory", dir, schemaVersion)
		}
		return nil
	} else if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+current+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	var version, objects int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read %s: %w", current, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return fmt.Errorf("read %s: %w", current, err)
	}
	switch {
	case version == schemaVersion, version == 0 && objects == 0:
		return nil
	case version > schemaVersion:
		return fmt.Errorf("%s is from a newer version (schema %d) than this binary (schema %d)", current, version, schemaVersion)
	default:
		return fmt.Errorf("%s is at schema %d; this version needs schema %d and does not upgrade older databases: use a new data directory", current, version, schemaVersion)
	}
}
