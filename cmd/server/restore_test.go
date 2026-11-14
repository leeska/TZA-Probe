package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leeska/TZA-ControlCenter/internal/backup"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

// backup-restore 用口令解开备份并解到新目录；口令错、目录非空都拒绝。
func TestRestoreCommand(t *testing.T) {
	src := t.TempDir()
	store, err := storage.Open(filepath.Join(src, backup.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "admin-token"), []byte("secret"), 0600)
	key, _ := backup.DeriveKey("restore passphrase", backup.Params{LogN: 10, R: 8, P: 1})
	file := filepath.Join(t.TempDir(), "tza-probe-20261114-030000.tzabk")
	f, _ := os.Create(file)
	w, _ := backup.NewWriter(f, key)
	if _, err := backup.Snapshot(context.Background(), store.DB(), src, w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	f.Close()
	store.Close()

	dst := filepath.Join(t.TempDir(), "restored")
	t.Setenv("TZA_BACKUP_PASSPHRASE", "not the passphrase")
	if err := restoreCommand([]string{"--in", file, "--data", dst}); err == nil {
		t.Fatal("口令错要失败")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("口令错时不能留下目录")
	}
	t.Setenv("TZA_BACKUP_PASSPHRASE", "restore passphrase")
	if err := restoreCommand([]string{"--in", file, "--data", dst}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "admin-token")); string(b) != "secret" {
		t.Fatal("文件要恢复出来")
	}
	if _, err := storage.Open(filepath.Join(dst, backup.DatabaseName)); err != nil {
		t.Fatal("恢复出的库要能打开", err)
	}
	if err := restoreCommand([]string{"--in", file, "--data", dst}); err == nil {
		t.Fatal("非空目录要拒绝")
	}
}
