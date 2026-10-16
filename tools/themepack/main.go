// Command themepack zips the official theme packages under themes/ into
// installable archives and checks each with the same validation the Server
// and TZA-CC use. scripts/pipeline.sh uses it for the ui-smoke run (which
// installs them on its disposable Server) and the release build; TZA-CC can
// import the folders directly (cc import --approve ../TZA-Probe/themes/*).
//
//	go run ./tools/themepack -out dist/themes themes/*
package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

func main() {
	out := flag.String("out", "dist/themes", "directory the <id>.zip files are written to")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "用法：themepack [-out 目录] <主题目录>…")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	failed := 0
	for _, dir := range flag.Args() {
		if err := pack(dir, *out); err != nil {
			fmt.Fprintf(os.Stderr, "%s：%v\n", dir, err)
			failed++
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func pack(dir, out string) error {
	archive, err := zipDir(dir)
	if err != nil {
		return err
	}
	m, _, err := tzapkg.Open(archive)
	if err != nil {
		return err
	}
	name := filepath.Join(out, m.ID+".zip")
	if err := os.WriteFile(name, archive, 0644); err != nil {
		return err
	}
	fmt.Printf("%s v%s → %s（%d KB）\n", m.ID, m.Version, name, len(archive)>>10)
	return nil
}

// zipDir packs a folder's regular files with theme.json at the root, skipping
// dot files the way the Server and TZA-CC do, and the folder's README.md (the
// package's documentation lives in the repository, not in the zip).
func zipDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") || d.Name() == "README.md" {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
