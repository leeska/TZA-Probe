package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/leeska/TZA-ControlCenter/internal/channel"
)

// BackupHandlers serves the backup capability: store the Server's encrypted
// backups in a directory and keep the newest few.
func BackupHandlers() map[string]channel.Handler {
	return map[string]channel.Handler{channel.KindBackupWrite: writeBackup}
}

var backupName = regexp.MustCompile(`^tza-probe-[0-9]{8}-[0-9]{6}\.tzabk$`)

func writeBackup(_ context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.BackupWriteParams
	if err := json.Unmarshal(raw, &p); err != nil || !backupName.MatchString(p.Name) || p.Size <= 0 || len(p.SHA256) != 64 || p.Keep < 1 || p.Keep > channel.MaxBackupKeep {
		return channel.WriteLine(conn, channel.BackupWriteResult{Error: "备份请求无效"})
	}
	if err := certDir(p.Dir); err != nil {
		return channel.WriteLine(conn, channel.BackupWriteResult{Error: err.Error()})
	}
	if err := os.MkdirAll(p.Dir, 0700); err != nil {
		return channel.WriteLine(conn, channel.BackupWriteResult{Error: "建目录失败：" + err.Error()})
	}
	part := filepath.Join(p.Dir, "."+p.Name+".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return channel.WriteLine(conn, channel.BackupWriteResult{Error: err.Error()})
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(conn, p.Size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != p.Size {
		err = errors.New("传输中断")
	}
	if err == nil && hex.EncodeToString(sum.Sum(nil)) != p.SHA256 {
		err = errors.New("校验和不符")
	}
	if err != nil {
		os.Remove(part)
		return channel.WriteLine(conn, channel.BackupWriteResult{Error: "写入备份失败：" + err.Error()})
	}
	if err := os.Rename(part, filepath.Join(p.Dir, p.Name)); err != nil {
		os.Remove(part)
		return channel.WriteLine(conn, channel.BackupWriteResult{Error: err.Error()})
	}
	kept, err := pruneBackups(p.Dir, p.Keep)
	if err != nil {
		return channel.WriteLine(conn, channel.BackupWriteResult{Error: "备份已写入，清理旧备份失败：" + err.Error()})
	}
	return channel.WriteLine(conn, channel.BackupWriteResult{Kept: kept})
}

// pruneBackups keeps the newest keep backups (by name, which sorts by time)
// and removes the rest; other files in dir are left alone.
func pruneBackups(dir string, keep int) ([]channel.BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if e.Type().IsRegular() && backupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	kept := []channel.BackupFile{}
	for i, name := range names {
		path := filepath.Join(dir, name)
		if i >= keep {
			if err := os.Remove(path); err != nil {
				return kept, err
			}
			continue
		}
		if info, err := os.Stat(path); err == nil {
			kept = append(kept, channel.BackupFile{Name: name, Size: info.Size(), Modified: info.ModTime().Unix()})
		}
	}
	return kept, nil
}
