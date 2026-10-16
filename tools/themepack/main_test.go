package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// 仓库里的官方主题包（复古印刷 / 二次元；现代极简已编进二进制）是发到 TZA-CC 的源码：每个都要能被
// Server 与 CC 共用的校验收下，装进 Store 后皮肤 id 与样式表都对得上。
func TestOfficialThemesInstall(t *testing.T) {
	dirs, err := filepath.Glob("../../themes/*")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("没找到 themes/ 下的主题包: %v", err)
	}
	store := &theme.Store{Dir: t.TempDir()}
	seen := map[string]bool{}
	for _, dir := range dirs {
		archive, err := zipDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		m, files, err := tzapkg.Open(archive)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if m.ID != filepath.Base(dir) || m.Type != tzapkg.TypeSkin || m.Version == "" {
			t.Errorf("%s: id / type / version 不对: %+v", dir, m)
		}
		for _, style := range m.Styles {
			if _, ok := files[style]; !ok {
				t.Errorf("%s: 样式表 %s 不在包里", dir, style)
			}
		}
		if _, err := store.Install(archive); err != nil {
			t.Fatalf("%s 装不上: %v", dir, err)
		}
		seen[m.ID] = true
	}
	for _, id := range []string{"press", "anime"} {
		if !seen[id] || !store.Known(id) {
			t.Errorf("官方主题包 %q 缺失或装完不认", id)
		}
	}
	// 打包工具写出的文件名就是包 id。
	out := t.TempDir()
	if err := pack("../../themes/press", out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "press.zip")); err != nil {
		t.Fatal(err)
	}
}
