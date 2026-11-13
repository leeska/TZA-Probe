package storage

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
)

// legacyDatabase writes what v2.0.0 left behind: schema.sql at user_version 30.
func legacyDatabase(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema + "\nINSERT INTO settings VALUES('legacy','kept');\nPRAGMA user_version=30;"); err != nil {
		t.Fatal(err)
	}
}

func extra(name string, up func(*MigrationContext) error) []Migration {
	return append(CoreMigrations(), Migration{Module: "demo", Version: 1, Name: name, Up: up})
}

func TestLegacyDatabaseUpgradesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tza-probe.db")
	legacyDatabase(t, path)
	if err := CheckDataDir(dir, CoreMigrations()); err != nil {
		t.Fatalf("v2.0.0 database refused by the pre-upgrade check: %v", err)
	}
	ran := 0
	store, err := OpenWith(path, extra("demo table", func(c *MigrationContext) error {
		ran++
		if c.DataDir != dir {
			t.Errorf("data dir %q", c.DataDir)
		}
		_, err := c.Tx.Exec("CREATE TABLE demo(x)")
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	var version int
	var value string
	store.db.QueryRow("PRAGMA user_version").Scan(&version)
	store.db.QueryRow("SELECT value FROM settings WHERE key='legacy'").Scan(&value)
	done, err := applied(store.db)
	store.Close()
	if err != nil || version != schemaVersion || value != "kept" || done["core"] != len(CoreMigrations()) || done["demo"] != 1 || ran != 1 {
		t.Fatalf("version=%d value=%q done=%v ran=%d err=%v", version, value, done, ran, err)
	}
	// A second start runs nothing again.
	store, err = OpenWith(path, extra("demo table", func(*MigrationContext) error { ran++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if ran != 1 {
		t.Fatal("migration ran twice")
	}
}

func TestFailedMigrationRollsBackAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tza-probe.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	_, err = OpenWith(path, extra("broken", func(c *MigrationContext) error {
		c.Tx.Exec("CREATE TABLE half(x)")
		return errors.New("boom")
	}))
	if err == nil || !strings.Contains(err.Error(), "demo/1") {
		t.Fatalf("broken migration: %v", err)
	}
	store, err = OpenWith(path, extra("fixed", func(c *MigrationContext) error {
		_, err := c.Tx.Exec("CREATE TABLE half(x)")
		return err
	}))
	if err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	store.Close()
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tza-probe.db")
	store, err := OpenWith(path, extra("demo", func(*MigrationContext) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	store.db.Exec("INSERT INTO schema_migrations VALUES('core',?,'future',0)", len(CoreMigrations())+1)
	store.Close()
	if err := CheckDataDir(dir, CoreMigrations()); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("check: %v", err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("open: %v", err)
	}
	// A module this binary does not ship is not a reason to refuse.
	store2, err := OpenWith(filepath.Join(t.TempDir(), "x.db"), extra("demo", func(*MigrationContext) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	store2.Close()
	if _, err := Open(filepath.Join(filepath.Dir(store2.path), "x.db")); err != nil {
		t.Fatalf("unknown module refused: %v", err)
	}
}

func TestMigrationListIsChecked(t *testing.T) {
	bad := [][]Migration{
		nil,
		{{Module: "demo", Version: 1, Up: func(*MigrationContext) error { return nil }}},
		append(CoreMigrations(), Migration{Module: "demo", Version: 2, Up: func(*MigrationContext) error { return nil }}),
	}
	for i, list := range bad {
		if err := checkMigrations(list); err == nil {
			t.Errorf("list %d accepted", i)
		}
	}
}

func TestAuditLog(t *testing.T) {
	store := openTest(t)
	ctx := t.Context()
	for i, action := range []string{"login", "shell.exec", "PUT /api/v1/admin/nodes/{id}"} {
		node := ""
		if i == 1 {
			node = "n1"
		}
		if err := store.Audit(ctx, model.AuditEntry{Actor: "admin", Source: "1.2.3.4", Action: action, NodeID: node, Status: 200, Detail: "uptime"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.AuditLog(ctx, 0, 10, "")
	if err != nil || len(all) != 3 || all[0].Action != "PUT /api/v1/admin/nodes/{id}" || all[0].At == 0 {
		t.Fatalf("%+v %v", all, err)
	}
	page, _ := store.AuditLog(ctx, all[0].ID, 1, "")
	if len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("paging %+v", page)
	}
	node, _ := store.AuditLog(ctx, 0, 10, "n1")
	if len(node) != 1 || node[0].Action != "shell.exec" {
		t.Fatalf("node filter %+v", node)
	}
}
