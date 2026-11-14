package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DatabaseName is the database file inside the data directory and the
// archive.
const DatabaseName = "tza-probe.db"

// skipped reports whether a data directory entry stays out of the archive:
// the live database files (a consistent copy goes in instead), caches that
// are fetched again, sessions, and the module's own work files.
func skipped(rel string, dir bool) bool {
	base := filepath.Base(rel)
	switch {
	case rel == DatabaseName, rel == DatabaseName+"-wal", rel == DatabaseName+"-shm", rel == DatabaseName+"-journal":
		return true
	case rel == "kernels" && dir: // 代理内核：主控需要时重新下载
		return true
	case rel == "admin-sessions", rel == "admin-sessions.tmp":
		return true
	case strings.HasPrefix(base, ".backup-"):
		return true
	case strings.HasSuffix(base, ".migrated"):
		return true
	}
	return false
}

// Snapshot writes a tar.gz of the data directory to w: a consistent copy of
// the database made with VACUUM INTO while the Server runs, then the other
// files. It returns the database's size.
func Snapshot(ctx context.Context, db *sql.DB, dataDir string, w io.Writer) (int64, error) {
	copyPath := filepath.Join(dataDir, ".backup-snapshot.db")
	os.Remove(copyPath)
	defer os.Remove(copyPath)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", copyPath); err != nil {
		return 0, fmt.Errorf("数据库快照失败：%w", err)
	}
	zw, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return 0, err
	}
	tw := tar.NewWriter(zw)
	info, err := os.Stat(copyPath)
	if err != nil {
		return 0, err
	}
	if err := addFile(tw, copyPath, DatabaseName, info); err != nil {
		return 0, err
	}
	err = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dataDir, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if skipped(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: rel + "/", Mode: 0700, ModTime: info.ModTime()})
		}
		if !info.Mode().IsRegular() {
			return nil // sockets, symlinks: nothing a restore needs
		}
		return addFile(tw, path, rel, info)
	})
	if err != nil {
		return 0, err
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	return info.Size(), zw.Close()
}

func addFile(tw *tar.Writer, path, name string, info fs.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: info.Size(), Mode: 0600, ModTime: info.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// Extract unpacks a decrypted backup into dir, which must not exist or be
// empty. Files get 0600 and directories 0700, as in a data directory.
func Extract(r io.Reader, dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s 不是空目录：恢复只写到新的或空的数据目录", dir)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("备份内容无法解压：%w", err)
	}
	tr := tar.NewReader(zr)
	sawDB := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(strings.TrimSuffix(h.Name, "/"))
		if name == "" || filepath.IsAbs(name) || name != filepath.Clean(name) || strings.HasPrefix(name, "..") {
			return fmt.Errorf("备份里有不安全的路径：%q", h.Name)
		}
		target := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			sawDB = sawDB || name == DatabaseName
		}
	}
	if !sawDB {
		return errors.New("备份里没有数据库")
	}
	return nil
}
