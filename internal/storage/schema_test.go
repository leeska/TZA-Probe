package storage

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsupportedDatabaseIsPreserved(t *testing.T) {
	for version := 0; version <= schemaVersion+1; version++ {
		if version == legacySchemaVersion {
			continue
		}
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(fmt.Sprintf("CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES('keep'); PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			if store, err := Open(path); err == nil {
				store.Close()
				t.Fatal("unsupported database was accepted")
			}
			var actualVersion, objects int
			var value string
			if err := db.QueryRow("PRAGMA user_version").Scan(&actualVersion); err != nil || actualVersion != version {
				t.Fatal("version changed", actualVersion, err)
			}
			if err := db.QueryRow("SELECT value FROM sentinel").Scan(&value); err != nil || value != "keep" {
				t.Fatal("existing data changed", value, err)
			}
			if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil || objects != 1 {
				t.Fatal("rejected database was initialized", objects, err)
			}
		})
	}
}

func TestDatabaseInitializationRollsBackAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	original := schema
	t.Cleanup(func() { schema = original })
	schema += "\nINVALID SQL;"
	if store, err := Open(path); err == nil {
		store.Close()
		t.Fatal("invalid schema was accepted")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var objects, version int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE " + userObjects).Scan(&objects); err != nil || objects != 0 {
		t.Fatal("failed initialization retained partial schema", objects, err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatal("failed initialization advanced version", version, err)
	}
	schema = original
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var integrity string
	if err := store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("initialized database is inconsistent", integrity, err)
	}
}

func TestFreshDatabaseHasNoLegacyRuntimeTables(t *testing.T) {
	store := openTest(t)
	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND (name LIKE 'executor_%' OR name LIKE 'runtime_%' OR name LIKE 'component_%')").Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy tables retained", count, err)
	}
}

func TestCheckDataDir(t *testing.T) {
	fresh := t.TempDir()
	if err := CheckDataDir(fresh, CoreMigrations()); err != nil {
		t.Fatalf("empty directory: %v", err)
	}
	current := t.TempDir()
	s, err := Open(filepath.Join(current, "tza-probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := CheckDataDir(current, CoreMigrations()); err != nil {
		t.Fatalf("current schema: %v", err)
	}
	legacy := t.TempDir()
	os.WriteFile(filepath.Join(legacy, "tza-v2.db"), []byte("x"), 0600)
	if err := CheckDataDir(legacy, CoreMigrations()); err == nil {
		t.Fatal("tza-v2.db accepted")
	}
	old := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(old, "tza-probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE nodes(id TEXT)")
	db.Exec("PRAGMA user_version=17")
	db.Close()
	before, _ := os.ReadFile(filepath.Join(old, "tza-probe.db"))
	if err := CheckDataDir(old, CoreMigrations()); err == nil || !strings.Contains(err.Error(), "schema 17") {
		t.Fatalf("schema 17: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(old, "tza-probe.db"))
	if !bytes.Equal(before, after) {
		t.Fatal("the check changed the old database")
	}
}
