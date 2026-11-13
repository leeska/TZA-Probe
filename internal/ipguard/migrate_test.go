package ipguard

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/leeska/TZA-Probe/internal/storage"
)

// A v2.0.0 data directory: main database at schema 30 plus ipguard.db with
// the old table names. The upgrade copies policies and jobs into the main
// database and keeps the old file as ipguard.db.migrated.
func TestMigrationImportsLegacyFile(t *testing.T) {
	dir := t.TempDir()
	old, err := sql.Open("sqlite", "file:"+filepath.Join(dir, LegacyFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE policies(node_id TEXT PRIMARY KEY, revision INTEGER NOT NULL, value TEXT NOT NULL);
CREATE TABLE jobs(id TEXT PRIMARY KEY,node_id TEXT NOT NULL,service TEXT NOT NULL,phase TEXT NOT NULL,policy TEXT NOT NULL,state TEXT NOT NULL,created_at INTEGER NOT NULL,started_at INTEGER NOT NULL DEFAULT 0,finished_at INTEGER NOT NULL DEFAULT 0,lease TEXT NOT NULL DEFAULT '',result TEXT NOT NULL DEFAULT '',dedupe TEXT NOT NULL UNIQUE);
INSERT INTO policies VALUES('n1',3,'{"enabled":true,"revision":3}');
INSERT INTO jobs VALUES('j1','n1','google','baseline','{"revision":3}','completed',100,101,102,'','{}','j1');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	list := append(storage.CoreMigrations(), Migrations()...)
	store, err := storage.OpenWith(filepath.Join(dir, "tza-probe.db"), list)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	guard := New(store.DB())
	policies, err := guard.Policies(context.Background())
	if err != nil || policies["n1"].Revision != 3 || !policies["n1"].Enabled {
		t.Fatalf("policies %+v %v", policies, err)
	}
	jobs, err := guard.Jobs(context.Background(), "n1")
	if err != nil || len(jobs) != 1 || jobs[0].ID != "j1" || jobs[0].State != "completed" {
		t.Fatalf("jobs %+v %v", jobs, err)
	}
	if _, err := os.Stat(filepath.Join(dir, LegacyFile)); !os.IsNotExist(err) {
		t.Fatal("legacy file still in place", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LegacyFile+".migrated")); err != nil {
		t.Fatal("legacy file not kept", err)
	}
}

func TestMigrationWithoutLegacyFile(t *testing.T) {
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "tza-probe.db"), append(storage.CoreMigrations(), Migrations()...))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if policies, err := New(store.DB()).Policies(context.Background()); err != nil || len(policies) != 0 {
		t.Fatal(policies, err)
	}
}
