package storage

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// schemaVersion marks a migration-managed database (user_version); the
// actual state lives in schema_migrations. legacySchemaVersion is the v2.0.0
// baseline, which upgrades in place to core migration 1. A v2.0.0 binary sees
// 31 as "newer" and refuses it, so rolling back after an upgrade needs the
// backup taken before it.
const (
	schemaVersion       = 31
	legacySchemaVersion = 30
)

//go:embed schema.sql
var schema string

func probeKeyExpr(prefix string) string {
	field := func(key string) string {
		return "COALESCE(json_extract(" + prefix + "task,'$." + key + "'),'')"
	}
	return "COALESCE(" + prefix + `task_id,'')||'|'||` + field("kind") + "||'|'||" + field("family") + "||'|'||" +
		field("host") + "||'|'||" + field("port") + "||'|'||" + field("region") + "||'|'||" + field("carrier")
}

// CheckDataDir reports, without creating or changing anything, whether this
// binary (with the given migrations) can run on the data directory dir: no
// database yet, a v2.0.0 database, or a migration-managed one no newer than
// the binary. The installer runs it with the new binary before replacing the
// old one, so an upgrade that would refuse to start stops while the running
// server is still untouched.
func CheckDataDir(dir string, list []Migration) error {
	current := filepath.Join(dir, "tza-probe.db")
	if _, err := os.Stat(current); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(dir, "tza-v2.db")); err == nil {
			return fmt.Errorf("%s holds an older database (tza-v2.db) that this version cannot upgrade: use a new data directory", dir)
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
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE " + userObjects).Scan(&objects); err != nil {
		return fmt.Errorf("read %s: %w", current, err)
	}
	switch {
	case version == legacySchemaVersion, version == 0 && objects == 0:
		return nil
	case version == schemaVersion:
		done, err := applied(db)
		if err != nil {
			return fmt.Errorf("read %s: %w", current, err)
		}
		if err := newerThanBinary(done, latest(list)); err != nil {
			return fmt.Errorf("%s: %w", current, err)
		}
		return nil
	case version > schemaVersion:
		return fmt.Errorf("%s is from a newer version (schema %d) than this binary (schema %d)", current, version, schemaVersion)
	default:
		return fmt.Errorf("%s is at schema %d; this version upgrades only from schema %d (v2.0.0): use a new data directory", current, version, legacySchemaVersion)
	}
}
