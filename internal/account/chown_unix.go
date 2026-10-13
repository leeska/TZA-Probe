//go:build unix

package account

import (
	"os"
	"syscall"
)

// OwnLikeDir gives file the owner and group of dir. Only root can do that,
// and only root needs to: anyone else already writes files they own.
func OwnLikeDir(file, dir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return os.Chown(file, int(st.Uid), int(st.Gid))
}
