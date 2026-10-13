package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A backup is a standalone database that opens on its own and holds the data,
// and taking it leaves nothing behind beside the live database.
func TestBackupIsAStandaloneCopy(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	path, cleanup, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	views, err := copied.Nodes(ctx, time.Now())
	copied.Close()
	if err != nil || len(views) != 1 || views[0].ID != id {
		t.Fatal("backup does not hold the node", views, err)
	}
	cleanup()
	entries, _ := os.ReadDir(filepath.Dir(s.path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".backup-") {
			t.Fatal("backup directory left behind:", e.Name())
		}
	}
}
