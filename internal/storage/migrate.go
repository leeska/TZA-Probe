package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// Migration is one versioned change to the database, owned by a module.
// Versions count up from 1 within a module; each runs once, in its own
// transaction, and is recorded in schema_migrations. Modules run in the order
// they are listed, core first, so a module may rely on core's tables.
type Migration struct {
	Module  string
	Version int
	Name    string
	Up      func(*MigrationContext) error
}

// MigrationContext is what a migration may touch: the transaction, the data
// directory (to import from an older side file), and AfterCommit for file
// moves that must only happen once the rows are safely committed.
type MigrationContext struct {
	Tx      *sql.Tx
	DataDir string
	after   []func() error
}

// AfterCommit runs fn once the migration's transaction has committed. A
// failure is logged but does not undo the migration or stop the server.
func (c *MigrationContext) AfterCommit(fn func() error) { c.after = append(c.after, fn) }

// CoreMigrations are core's own migrations. Version 1 is the schema 30
// baseline that every database created by v2.0.0 already has.
func CoreMigrations() []Migration {
	return []Migration{
		{Module: "core", Version: 1, Name: "baseline (schema 30)", Up: func(c *MigrationContext) error {
			_, err := c.Tx.Exec(schema)
			return err
		}},
		{Module: "core", Version: 2, Name: "audit log", Up: func(c *MigrationContext) error {
			_, err := c.Tx.Exec(auditTable)
			return err
		}},
	}
}

const migrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations(module TEXT NOT NULL, version INTEGER NOT NULL, name TEXT NOT NULL, applied_at INTEGER NOT NULL, PRIMARY KEY(module, version))`

// checkMigrations rejects a list a binary could not run safely: core must come
// first, and versions within a module must count 1, 2, 3 ... in order.
func checkMigrations(list []Migration) error {
	if len(list) == 0 || list[0].Module != "core" || list[0].Version != 1 {
		return errors.New("migrations must start with core version 1")
	}
	next := map[string]int{}
	for _, m := range list {
		if m.Module == "" || m.Up == nil {
			return fmt.Errorf("migration %s/%d is incomplete", m.Module, m.Version)
		}
		if m.Version != next[m.Module]+1 {
			return fmt.Errorf("migration %s/%d is out of order", m.Module, m.Version)
		}
		next[m.Module] = m.Version
	}
	return nil
}

// latest is the highest version per module that this binary knows.
func latest(list []Migration) map[string]int {
	known := map[string]int{}
	for _, m := range list {
		known[m.Module] = m.Version
	}
	return known
}

// applied reads schema_migrations. A database that has none recorded yet
// returns an empty map.
func applied(q interface {
	Query(string, ...any) (*sql.Rows, error)
}) (map[string]int, error) {
	rows, err := q.Query("SELECT module, max(version) FROM schema_migrations GROUP BY module")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	done := map[string]int{}
	for rows.Next() {
		var module string
		var version int
		if err := rows.Scan(&module, &version); err != nil {
			return nil, err
		}
		done[module] = version
	}
	return done, rows.Err()
}

// newerThanBinary names the first module the database has run further than
// this binary knows. Modules the binary does not ship at all are ignored:
// their tables are simply unused.
func newerThanBinary(done, known map[string]int) error {
	for module, version := range done {
		if max, ok := known[module]; ok && version > max {
			return fmt.Errorf("database has %s migration %d but this binary only knows up to %d: it is from a newer version", module, version, max)
		}
	}
	return nil
}

// migrate brings db up to date with list. It accepts three starting points:
// an empty database, a v2.0.0 database (user_version 30, no migration table:
// core version 1 already applied) and a migration-managed one.
func migrate(db *sql.DB, dataDir string, list []Migration) error {
	if err := checkMigrations(list); err != nil {
		return err
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch {
	case version == schemaVersion:
	case version > schemaVersion:
		return errors.New("database version is newer than this binary")
	case version == legacySchemaVersion:
		// v2.0.0 baseline: record core/1 as done and switch to migrations.
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(migrationsTable); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations VALUES('core',1,?,?)", list[0].Name, time.Now().Unix()); err != nil {
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	case version == 0:
		// A fresh database. user_version is set only after every migration
		// succeeded, so a failed first start leaves it at 0 and the next start
		// simply tries again (the empty migration table does not count).
		var objects int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE " + userObjects).Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			return errors.New("database is not empty; use a new data directory")
		}
		if _, err := db.Exec(migrationsTable); err != nil {
			return err
		}
	default:
		return fmt.Errorf("database schema %d is unsupported; use a new data directory", version)
	}
	done, err := applied(db)
	if err != nil {
		return err
	}
	if err := newerThanBinary(done, latest(list)); err != nil {
		return err
	}
	for _, m := range list {
		if m.Version <= done[m.Module] {
			continue
		}
		if err := runMigration(db, dataDir, m); err != nil {
			return fmt.Errorf("migration %s/%d (%s): %w", m.Module, m.Version, m.Name, err)
		}
	}
	if version == 0 {
		_, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion))
	}
	return err
}

// userObjects matches what counts as content when deciding a database is
// empty: anything but SQLite's own objects and the migration table.
const userObjects = "name NOT LIKE 'sqlite_%' AND name NOT LIKE 'sqlite_autoindex_schema_migrations%' AND name <> 'schema_migrations'"

func runMigration(db *sql.DB, dataDir string, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c := &MigrationContext{Tx: tx, DataDir: dataDir}
	if err := m.Up(c); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations VALUES(?,?,?,?)", m.Module, m.Version, m.Name, time.Now().Unix()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, fn := range c.after {
		if err := fn(); err != nil {
			log.Printf("migration %s/%d: after commit: %v", m.Module, m.Version, err)
		}
	}
	return nil
}
