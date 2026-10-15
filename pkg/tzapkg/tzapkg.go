// Package tzapkg is the TZA package format: a zip holding theme.json plus the
// stylesheets, images and fonts it references. The Probe server installs these
// packages and TZA-CC reviews and distributes them; both validate with this
// package so a package one side accepts the other side accepts too.
//
// A package carries no script: the panel CSP only runs the panel's own
// JavaScript, and a package's stylesheets load from the panel's origin, so
// they cannot reach other hosts either. See docs/THEMES.md.
package tzapkg

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Limits for one package. The archive itself is capped at MaxUpload; the rest
// bound what it may expand to.
const (
	MaxUpload     = 20 << 20
	maxUnpacked   = 48 << 20
	maxFile       = 16 << 20
	maxFiles      = 300
	maxStyles     = 8
	maxCharacters = 60
)

// Colors are the two browser-chrome colours (meta theme-color) of a skin.
type Colors struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// Package types: a skin is a selectable look (stylesheets); a character pack
// adds characters to the 二次元 skin (the official "anime" package) and is not
// a skin of its own.
const (
	TypeSkin       = "skin"
	TypeCharacters = "characters"
)

// Manifest describes one package. Builtin, Rev and Source are set by the Probe
// server, never read from an uploaded theme.json.
type Manifest struct {
	ID         string      `json:"id"`
	Type       string      `json:"type,omitempty"`
	Name       string      `json:"name"`
	Hint       string      `json:"hint,omitempty"`
	Version    string      `json:"version,omitempty"`
	Author     string      `json:"author,omitempty"`
	Chrome     Colors      `json:"chrome,omitempty"`
	Swatch     []string    `json:"swatch,omitempty"`
	Styles     []string    `json:"styles,omitempty"`
	Characters []Character `json:"characters,omitempty"`
	Builtin    bool        `json:"builtin,omitempty"`
	Rev        string      `json:"rev,omitempty"`
	// Source is SourceMarket for a package installed from TZA-CC, empty for a
	// local upload.
	Source string `json:"source,omitempty"`
}

// SourceMarket marks a package installed from the TZA-CC market.
const SourceMarket = "market"

// Character is one figure of a character pack: a full-body picture holding a
// board, drawn on the 二次元 skin's cards. Plate is where the board's top and
// bottom outer edges sit, as fractions of the picture height; the card lays
// its HTML board over that band. The panel picks a character per node: the
// node's own choice, then Nodes (exact node names), then Regions, then the
// pack's Default.
type Character struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Regions []string `json:"regions,omitempty"`
	Nodes   []string `json:"nodes,omitempty"`
	Default bool     `json:"default,omitempty"`
	Art     string   `json:"art"`
	Hands   string   `json:"hands,omitempty"`
	Blink   string   `json:"blink,omitempty"`
	Plate   Plate    `json:"plate"`
	Palette struct {
		Light Palette `json:"light"`
		Dark  Palette `json:"dark"`
	} `json:"palette"`
}

// Plate is the board band of a character picture (0–1 of its height).
type Plate struct {
	Top    float64 `json:"top"`
	Bottom float64 `json:"bottom"`
}

// Palette colours a character's card: board face, cell background, outline,
// progress track and accent. Cell may be left empty.
type Palette struct {
	Board  string `json:"board"`
	Cell   string `json:"cell,omitempty"`
	Line   string `json:"line"`
	Track  string `json:"track"`
	Accent string `json:"accent"`
}

// Reserved are the ids of the skins compiled into the panel; no package may
// take one. Only 液态玻璃 is compiled in; 复古印刷 / 现代极简 / 二次元 are the
// official packages under themes/ in this repository, installed from the market.
var Reserved = []string{"glass"}

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	hexColor    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	safePath    = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	contentType = map[string]string{
		".css":   "text/css; charset=utf-8",
		".json":  "application/json",
		".webp":  "image/webp",
		".png":   "image/png",
		".jpg":   "image/jpeg",
		".jpeg":  "image/jpeg",
		".gif":   "image/gif",
		".avif":  "image/avif",
		".woff2": "font/woff2",
		".woff":  "font/woff",
	}
)

// ValidID reports whether id is a well-formed package id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// SafePath reports whether p is a relative path a package may contain.
func SafePath(p string) bool { return safePath.MatchString(p) && !strings.Contains(p, "..") }

// ContentType is the served type of a package file, by extension; ok is false
// for a type packages may not carry.
func ContentType(name string) (ctype string, ok bool) {
	ctype, ok = contentType[strings.ToLower(path.Ext(name))]
	return
}

// Files unpacks an archive under the package rules (names, types, sizes,
// one shared top-level folder stripped) and returns its files by path,
// without interpreting any of them.
func Files(archive []byte) (map[string][]byte, error) {
	if len(archive) > MaxUpload {
		return nil, fmt.Errorf("压缩包最大 %d MB", MaxUpload>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, errors.New("不是有效的 zip 压缩包")
	}
	return entries(zr)
}

// Open validates a whole archive: names, types, sizes, the manifest and every
// file the manifest references. It returns the manifest and the unpacked files
// keyed by their path inside the package.
func Open(archive []byte) (Manifest, map[string][]byte, error) {
	if len(archive) > MaxUpload {
		return Manifest{}, nil, fmt.Errorf("压缩包最大 %d MB", MaxUpload>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Manifest{}, nil, errors.New("不是有效的 zip 压缩包")
	}
	files, err := entries(zr)
	if err != nil {
		return Manifest{}, nil, err
	}
	rawManifest, ok := files["theme.json"]
	if !ok {
		return Manifest{}, nil, errors.New("压缩包根目录（或唯一的顶层文件夹）里没有 theme.json")
	}
	if _, ok := files["plugin.json"]; ok {
		return Manifest{}, nil, errors.New("主题包不带 plugin.json；插件请单独打包")
	}
	m, err := Parse(rawManifest)
	if err != nil {
		return Manifest{}, nil, err
	}
	for _, p := range m.Styles {
		if _, ok := files[p]; !ok {
			return Manifest{}, nil, fmt.Errorf("styles 里的 %q 不在压缩包里", p)
		}
	}
	for _, c := range m.Characters {
		for _, p := range []string{c.Art, c.Hands, c.Blink} {
			if _, ok := files[p]; p != "" && !ok {
				return Manifest{}, nil, fmt.Errorf("角色 %s 引用的 %q 不在压缩包里", c.ID, p)
			}
		}
	}
	return m, files, nil
}

// Parse decodes and checks a theme.json; it never trusts Builtin / Rev / Source.
func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, errors.New("theme.json 不是有效的 JSON：" + err.Error())
	}
	m.Builtin, m.Rev, m.Source = false, "", ""
	m.Name, m.Hint, m.Version, m.Author = strings.TrimSpace(m.Name), strings.TrimSpace(m.Hint), strings.TrimSpace(m.Version), strings.TrimSpace(m.Author)
	if m.Type == "" {
		m.Type = TypeSkin
	}
	switch {
	case m.Type != TypeSkin && m.Type != TypeCharacters:
		return Manifest{}, errors.New(`type 须为 "skin"（主题）或 "characters"（角色包）`)
	case !idPattern.MatchString(m.ID):
		return Manifest{}, errors.New("id 须为 2–32 位小写字母、数字或连字符，以字母开头")
	case slices.Contains(Reserved, m.ID):
		return Manifest{}, fmt.Errorf("id %q 是内置主题，换一个名字", m.ID)
	case m.Name == "" || utf8.RuneCountInString(m.Name) > 30:
		return Manifest{}, errors.New("name 必填，最多 30 个字")
	case utf8.RuneCountInString(m.Hint) > 60, utf8.RuneCountInString(m.Version) > 20, utf8.RuneCountInString(m.Author) > 40:
		return Manifest{}, errors.New("hint 最多 60 字，version 最多 20 字，author 最多 40 字")
	}
	if m.Type == TypeCharacters {
		if len(m.Styles) > 0 {
			return Manifest{}, errors.New("角色包不带样式表（styles 留空）：它只给二次元主题添角色")
		}
		if len(m.Characters) == 0 || len(m.Characters) > maxCharacters {
			return Manifest{}, fmt.Errorf("角色包须有 1–%d 个角色（characters）", maxCharacters)
		}
		seen := map[string]bool{}
		for i := range m.Characters {
			if err := checkCharacter(&m.Characters[i]); err != nil {
				return Manifest{}, err
			}
			if seen[m.Characters[i].ID] {
				return Manifest{}, fmt.Errorf("角色 id %q 重复", m.Characters[i].ID)
			}
			seen[m.Characters[i].ID] = true
		}
		return m, nil
	}
	switch {
	case len(m.Characters) > 0:
		return Manifest{}, errors.New(`主题不带角色；角色请单独做成 "type": "characters" 的角色包`)
	case !hexColor.MatchString(m.Chrome.Light) || !hexColor.MatchString(m.Chrome.Dark):
		return Manifest{}, errors.New("chrome.light / chrome.dark 须为 #rrggbb 颜色")
	case len(m.Swatch) != 3:
		return Manifest{}, errors.New("swatch 须为 3 个 #rrggbb 颜色")
	case len(m.Styles) == 0 || len(m.Styles) > maxStyles:
		return Manifest{}, fmt.Errorf("styles 须列出 1–%d 个样式表", maxStyles)
	}
	for _, c := range m.Swatch {
		if !hexColor.MatchString(c) {
			return Manifest{}, errors.New("swatch 须为 3 个 #rrggbb 颜色")
		}
	}
	for _, p := range m.Styles {
		if !safePath.MatchString(p) || path.Ext(p) != ".css" || strings.Contains(p, "..") {
			return Manifest{}, fmt.Errorf("styles 里的 %q 须为包内的 .css 相对路径（只用字母、数字、. _ - /）", p)
		}
	}
	return m, nil
}

var characterID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// checkCharacter validates one character and normalizes its text fields.
func checkCharacter(c *Character) error {
	c.Name = strings.TrimSpace(c.Name)
	if !characterID.MatchString(c.ID) {
		return fmt.Errorf("角色 id %q 须为 1–32 位小写字母、数字或连字符，以字母开头", c.ID)
	}
	label := "角色 " + c.ID
	if c.Name == "" || utf8.RuneCountInString(c.Name) > 30 {
		return errors.New(label + "：name 必填，最多 30 个字")
	}
	if len(c.Regions) > 30 || len(c.Nodes) > 50 {
		return errors.New(label + "：regions 最多 30 个、nodes 最多 50 个")
	}
	for i, r := range c.Regions {
		c.Regions[i] = strings.TrimSpace(r)
		if c.Regions[i] == "" || len(c.Regions[i]) > 60 {
			return errors.New(label + "：regions 里每一项须为 1–60 字节的地区名")
		}
	}
	for i, n := range c.Nodes {
		c.Nodes[i] = strings.TrimSpace(n)
		if c.Nodes[i] == "" || len(c.Nodes[i]) > 100 {
			return errors.New(label + "：nodes 里每一项须为 1–100 字节的节点名")
		}
	}
	for field, p := range map[string]string{"art": c.Art, "hands": c.Hands, "blink": c.Blink} {
		if p == "" && field != "art" {
			continue
		}
		ext := strings.ToLower(path.Ext(p))
		if !safePath.MatchString(p) || strings.Contains(p, "..") || (ext != ".webp" && ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".avif") {
			return fmt.Errorf("%s：%s 须为包内的 webp / png / jpg / avif 图片相对路径", label, field)
		}
	}
	if c.Plate.Top < 0.1 || c.Plate.Top > 0.7 || c.Plate.Bottom < c.Plate.Top+0.03 || c.Plate.Bottom > 0.9 {
		return errors.New(label + "：plate.top 须在 0.1–0.7 之间，plate.bottom 须比 top 至少大 0.03 且不超过 0.9（牌子上下外沿占图高的比例）")
	}
	for name, p := range map[string]Palette{"light": c.Palette.Light, "dark": c.Palette.Dark} {
		for _, v := range []string{p.Board, p.Line, p.Track, p.Accent} {
			if !hexColor.MatchString(v) {
				return fmt.Errorf("%s：palette.%s 的 board / line / track / accent 须为 #rrggbb 颜色", label, name)
			}
		}
		if p.Cell != "" && !hexColor.MatchString(p.Cell) {
			return fmt.Errorf("%s：palette.%s.cell 须为 #rrggbb 颜色或留空", label, name)
		}
	}
	return nil
}

// entries reads every regular file of the archive into memory after checking
// names, types and sizes. A single top-level folder (how most zip tools pack a
// directory) is stripped so theme.json sits at the root.
func entries(zr *zip.Reader) (map[string][]byte, error) {
	type item struct {
		name string
		f    *zip.File
	}
	items := []item{}
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") || strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
			continue
		}
		if f.Mode()&fs.ModeType != 0 {
			return nil, fmt.Errorf("压缩包里的 %q 不是普通文件（不收符号链接）", name)
		}
		if !safePath.MatchString(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("压缩包里的文件名 %q 不合规：只用字母、数字、. _ - 和 /", name)
		}
		items = append(items, item{name, f})
	}
	if len(items) == 0 {
		return nil, errors.New("压缩包是空的")
	}
	if len(items) > maxFiles {
		return nil, fmt.Errorf("文件太多（最多 %d 个）", maxFiles)
	}
	// Strip one shared top-level folder.
	if first := strings.SplitN(items[0].name, "/", 2); len(first) == 2 {
		prefix := first[0] + "/"
		shared := true
		for _, it := range items {
			if !strings.HasPrefix(it.name, prefix) {
				shared = false
				break
			}
		}
		if shared {
			for i := range items {
				items[i].name = strings.TrimPrefix(items[i].name, prefix)
			}
		}
	}
	out := map[string][]byte{}
	var total int64
	for _, it := range items {
		ext := strings.ToLower(path.Ext(it.name))
		if _, ok := contentType[ext]; !ok {
			return nil, fmt.Errorf("不支持的文件类型 %q：只收 CSS、theme.json、webp / png / jpg / gif / avif 图片和 woff / woff2 字体（不收脚本、HTML、SVG）", it.name)
		}
		if ext == ".json" && it.name != "theme.json" && it.name != "plugin.json" {
			return nil, fmt.Errorf("除 theme.json / plugin.json 外不收 JSON 文件：%q", it.name)
		}
		if it.f.UncompressedSize64 > maxFile {
			return nil, fmt.Errorf("%q 太大（单个文件最多 %d MB）", it.name, maxFile>>20)
		}
		rc, err := it.f.Open()
		if err != nil {
			return nil, fmt.Errorf("读不出 %q：%v", it.name, err)
		}
		// Read one byte past the limit: a lying header cannot sneak a bigger file through.
		data, err := io.ReadAll(io.LimitReader(rc, maxFile+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("读不出 %q：%v", it.name, err)
		}
		if len(data) > maxFile {
			return nil, fmt.Errorf("%q 太大（单个文件最多 %d MB）", it.name, maxFile>>20)
		}
		total += int64(len(data))
		if total > maxUnpacked {
			return nil, fmt.Errorf("解压后太大（最多 %d MB）", maxUnpacked>>20)
		}
		if _, dup := out[it.name]; dup {
			return nil, fmt.Errorf("压缩包里有重名文件 %q", it.name)
		}
		out[it.name] = data
	}
	return out, nil
}
