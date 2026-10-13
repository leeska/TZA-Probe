package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabasePath(t *testing.T) {
	dir := t.TempDir()
	current, legacy := filepath.Join(dir, "tza-probe.db"), filepath.Join(dir, "tza-v2.db")
	if got := databasePath(dir); got != current {
		t.Fatalf("empty dir: %s", got)
	}
	if err := os.WriteFile(legacy, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := databasePath(dir); got != legacy {
		t.Fatalf("only the older file: %s", got)
	}
	// A restored backup under the current name wins over a leftover older file.
	if err := os.WriteFile(current, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := databasePath(dir); got != current {
		t.Fatalf("both files: %s", got)
	}
}
