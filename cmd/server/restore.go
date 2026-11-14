package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/leeska/TZA-Probe/internal/backup"
)

// restoreCommand decrypts a backup and unpacks it into a new data directory:
//
//	tza-server backup-restore --in tza-probe-20261114-030000.tzabk --data /var/lib/tza-probe-restored
//
// The passphrase comes from TZA_BACKUP_PASSPHRASE or is asked on the
// terminal. The directory must not exist or be empty; point the service at
// it (or move it into place) once it is there, then chown it to the service
// user.
func restoreCommand(args []string) error {
	fs := flag.NewFlagSet("tza-server backup-restore", flag.ContinueOnError)
	in := fs.String("in", "", "backup file (.tzabk)")
	dir := fs.String("data", "", "new data directory to restore into (must not exist or be empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *dir == "" {
		return errors.New("用法：tza-server backup-restore --in 备份文件 --data 新数据目录")
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	pass := os.Getenv("TZA_BACKUP_PASSPHRASE")
	if pass == "" {
		if pass, err = askPassphrase(); err != nil {
			return err
		}
	}
	r, err := backup.NewReader(bufio.NewReaderSize(f, 1<<20), pass)
	if err != nil {
		return err
	}
	if err := backup.Extract(r, *dir); err != nil {
		return err
	}
	// Read to the last chunk: a file cut short must not pass as complete.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return err
	}
	fmt.Printf("已恢复到 %s。\n下一步：chown -R tza-probe:tza-probe %s，再让 tza-server 用这个目录（--data）启动。\n", *dir, *dir)
	return nil
}

// askPassphrase reads a line from the terminal without echo.
func askPassphrase() (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errors.New("没有终端可以输入口令：设置环境变量 TZA_BACKUP_PASSPHRASE")
	}
	defer tty.Close()
	fmt.Fprint(tty, "备份口令：")
	off := exec.Command("stty", "-echo")
	off.Stdin = tty
	echoOff := off.Run() == nil
	line, err := bufio.NewReader(tty).ReadString('\n')
	if echoOff {
		on := exec.Command("stty", "echo")
		on.Stdin = tty
		on.Run()
	}
	fmt.Fprintln(tty)
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
