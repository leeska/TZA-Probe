package storage

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
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
