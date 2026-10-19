// Package tzapkg is the TZA package format: a zip holding theme.json plus the
// stylesheets, images and fonts it references. The Probe server installs these
// packages and TZA-CC reviews and distributes them; both validate with this
// package so a package one side accepts the other side accepts too.
//
// CSS skins extend the built-in UI; frontend themes own an isolated HTML/JS
// document and use the versioned public bridge. See docs/THEMES.md.
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
	MaxUpload       = 20 << 20
	maxUnpacked     = 48 << 20
	maxFile         = 16 << 20
	maxFiles        = 300
	maxStyles       = 8
	maxDecorations  = 60
	maxDecoImages   = 8
	maxDecoVars     = 32
	maxSkinSettings = 6
	maxSkinOptions  = 8
	maxPackToggles  = 100
)

// Colors are the two browser-chrome colours (meta theme-color) of a skin.
type Colors struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// Package types: a skin is a selectable CSS look or independent frontend; an add-on extends
// one skin (Manifest.For) with more decorations and is not a skin of its own.
// An add-on only takes effect while its skin is installed and in use.
const (
	TypeSkin  = "skin"
	TypeAddon = "addon"
)

// FrontendMinProbe is the first Probe version implementing the isolated API.
const FrontendMinProbe = "1.5.0"

// SettingsUIMinProbe is the first release supporting developer settings dialogs.
const SettingsUIMinProbe = "1.6.0"

// Frontend replaces the public UI with a package-owned document. API version 1
// uses the public-data bridge; it never grants access to administrator APIs.
type Frontend struct {
	Entry      string `json:"entry"`
	APIVersion int    `json:"api_version"`
}

// Manifest describes one package. Builtin, Rev and Source are set by the Probe
// server, never read from an uploaded theme.json.
type Manifest struct {
	ID       string    `json:"id"`
	Type     string    `json:"type,omitempty"`
	Name     string    `json:"name"`
	Hint     string    `json:"hint,omitempty"`
	Version  string    `json:"version,omitempty"`
	Author   string    `json:"author,omitempty"`
	Chrome   Colors    `json:"chrome,omitempty"`
	Swatch   []string  `json:"swatch,omitempty"`
	Styles   []string  `json:"styles,omitempty"`
	Frontend *Frontend `json:"frontend,omitempty"`
	// SettingsUI is a developer-owned settings dialog, isolated from the admin DOM.
	SettingsUI *Frontend `json:"settings_ui,omitempty"`
	// For is the skin an add-on extends (add-ons only).
	For string `json:"for,omitempty"`
	// Decorations are the per-node decorations a skin or add-on offers; the
	// panel picks one per card. See Decoration.
	Decorations []Decoration `json:"decorations,omitempty"`
	// Settings is the theme's own settings menu, declared by its developer:
	// what an administrator can switch for this look. See SkinSetting.
	Settings []SkinSetting `json:"settings,omitempty"`
	// Base retains legacy manifest compatibility. Material CSS is now package-owned.
	Base string `json:"base,omitempty"`
	// Layout declares card layout variants the panel applies while the skin
	// is on. Nil keeps the panel's defaults.
	Layout  *Layout `json:"layout,omitempty"`
	Builtin bool    `json:"builtin,omitempty"`
	Rev     string  `json:"rev,omitempty"`
	// Source is SourceMarket for a package installed from TZA-CC, empty for a
	// local upload.
	Source string `json:"source,omitempty"`
}

// Base layers a skin may stack on.
const (
	BaseGlass = "glass"
	BasePlain = "plain"
)

// Layout is a skin's declarative card layout. CardSections lists the card
// view's body sections in display order; a section left out is hidden. The
// panel renders it; the package still carries no script.
type Layout struct {
	CardSections []string `json:"card_sections,omitempty"`
	Density      string   `json:"density,omitempty"`
	DefaultView  string   `json:"default_view,omitempty"`
	// AlignRows false stops the panel from padding card sections so a row of
	// cards lines up (for skins whose cards are not a plain stack of sections).
	AlignRows *bool `json:"align_rows,omitempty"`
}

// CardSections are the section names Layout.CardSections may use: the
// region · group line, the four meters, the rate / expiry band and the
// network (latency / route) blocks.
var CardSections = []string{"node-sub", "metrics-grid", "card-stats", "network"}

// checkLayout validates a skin's layout declaration.
func checkLayout(l *Layout) error {
	if l == nil {
		return nil
	}
	if len(l.CardSections) > len(CardSections) {
		return fmt.Errorf("layout.card_sections 最多 %d 项", len(CardSections))
	}
	seen := map[string]bool{}
	for _, sec := range l.CardSections {
		if !slices.Contains(CardSections, sec) {
			return fmt.Errorf("layout.card_sections 里的 %q 不认识：只能用 %s", sec, strings.Join(CardSections, " / "))
		}
		if seen[sec] {
			return fmt.Errorf("layout.card_sections 里的 %q 重复", sec)
		}
		seen[sec] = true
	}
	switch l.Density {
	case "", "cozy", "compact":
	default:
		return fmt.Errorf(`layout.density 须为 "cozy" 或 "compact"，不是 %q`, l.Density)
	}
	switch l.DefaultView {
	case "", "card", "list":
	default:
		return fmt.Errorf(`layout.default_view 须为 "card" 或 "list"，不是 %q`, l.DefaultView)
	}
	return nil
}

// SkinSetting is one entry of a theme's settings menu. The administrator's
// choice applies to every visitor: while the theme is on, the panel writes it
// to <html data-opt-<id>="<value>"> so the theme's stylesheets can select on
// it. A toggle's values are "on" / "off"; a choice's are its option values.
// SettingAddons switches the skin's decoration sources on and off: its value
// is the ids switched off, the skin's own id standing for its own decorations.
type SkinSetting struct {
	ID      string       `json:"id"`
	Type    string       `json:"type"`
	Label   string       `json:"label"`
	Hint    string       `json:"hint,omitempty"`
	Options []SkinOption `json:"options,omitempty"`
	Default string       `json:"default,omitempty"`
}

// SkinOption is one value of a choice setting.
type SkinOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Skin setting types.
const (
	SettingChoice = "choice"
	SettingToggle = "toggle"
	SettingAddons = "addons"
)

var skinSettingID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// checkSkinSettings validates a skin's settings menu and fills in defaults: a
// choice defaults to its first option, a toggle to "off".
func checkSkinSettings(list []SkinSetting) error {
	if len(list) > maxSkinSettings {
		return fmt.Errorf("settings 最多 %d 项", maxSkinSettings)
	}
	seen := map[string]bool{}
	addons := false
	for i := range list {
		st := &list[i]
		st.Label, st.Hint = strings.TrimSpace(st.Label), strings.TrimSpace(st.Hint)
		label := "settings 里的 " + st.ID
		switch {
		case !skinSettingID.MatchString(st.ID):
			return fmt.Errorf("settings 的 id %q 须为 1–24 位小写字母、数字或连字符，以字母开头", st.ID)
		case seen[st.ID]:
			return fmt.Errorf("settings 的 id %q 重复", st.ID)
		case st.Label == "" || utf8.RuneCountInString(st.Label) > 20:
			return errors.New(label + "：label 必填，最多 20 个字")
		case utf8.RuneCountInString(st.Hint) > 60:
			return errors.New(label + "：hint 最多 60 个字")
		}
		seen[st.ID] = true
		switch st.Type {
		case SettingToggle:
			if len(st.Options) > 0 {
				return errors.New(label + "：toggle 不带 options")
			}
			if st.Default == "" {
				st.Default = "off"
			}
			if st.Default != "on" && st.Default != "off" {
				return errors.New(label + `：toggle 的 default 须为 "on" 或 "off"`)
			}
		case SettingChoice:
			if len(st.Options) < 2 || len(st.Options) > maxSkinOptions {
				return fmt.Errorf("%s：choice 须有 2–%d 个 options", label, maxSkinOptions)
			}
			values := map[string]bool{}
			for j := range st.Options {
				o := &st.Options[j]
				o.Label = strings.TrimSpace(o.Label)
				if !skinSettingID.MatchString(o.Value) || values[o.Value] {
					return fmt.Errorf("%s：option 的 value %q 须为互不重复的 1–24 位小写字母、数字或连字符", label, o.Value)
				}
				if o.Label == "" || utf8.RuneCountInString(o.Label) > 20 {
					return errors.New(label + "：每个 option 的 label 必填，最多 20 个字")
				}
				values[o.Value] = true
			}
			if st.Default == "" {
				st.Default = st.Options[0].Value
			}
			if !values[st.Default] {
				return fmt.Errorf("%s：default %q 不在 options 里", label, st.Default)
			}
		case SettingAddons:
			if len(st.Options) > 0 || st.Default != "" {
				return errors.New(label + "：addons 不带 options / default")
			}
			if addons {
				return errors.New(label + `：type "addons" 最多一项`)
			}
			addons = true
		default:
			return errors.New(label + `：type 须为 "choice"、"toggle" 或 "addons"`)
		}
	}
	return nil
}

// SkinOptionValues checks one administrator patch against a skin's settings
// and returns the normalized values: a toggle or choice is a string, an
// addons setting is the list of package ids switched off. Unknown keys are
// refused so a typo never silently does nothing.
func SkinOptionValues(settings []SkinSetting, patch map[string]json.RawMessage) (map[string]any, error) {
	out := map[string]any{}
	for key, raw := range patch {
		var st *SkinSetting
		for i := range settings {
			if settings[i].ID == key {
				st = &settings[i]
			}
		}
		if st == nil {
			return nil, fmt.Errorf("这个主题没有设置项 %q", key)
		}
		if st.Type == SettingAddons {
			var off []string
			if err := json.Unmarshal(raw, &off); err != nil || len(off) > maxPackToggles {
				return nil, fmt.Errorf("%s 须为包 id 列表（最多 %d 个）", st.Label, maxPackToggles)
			}
			clean := []string{}
			for _, id := range off {
				if idPattern.MatchString(id) && !slices.Contains(clean, id) {
					clean = append(clean, id)
				}
			}
			out[key] = clean
			continue
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s 的值须为字符串", st.Label)
		}
		ok := st.Type == SettingToggle && (v == "on" || v == "off")
		for _, o := range st.Options {
			ok = ok || o.Value == v
		}
		if !ok {
			return nil, fmt.Errorf("%s 不能设为 %q", st.Label, v)
		}
		out[key] = v
	}
	return out, nil
}

// SourceMarket marks a package installed from the TZA-CC market.
const SourceMarket = "market"

// Decoration is one per-node decoration of a skin or add-on. The panel picks
// one per card: the node's own choice ("<package>/<id>"), then the first item
// whose Nodes holds the node name, then Regions holds its region, then the
// first Default — add-ons before the skin's own items at every step, and the
// skin's own Default as the last resort. The chosen card gets
// data-deco="<package>/<id>" and data-deco-pack="<package>", and every image
// and var becomes a custom property on it: --deco-<name> (images as url()).
// The panel attaches no meaning to the names; the skin's stylesheet does.
type Decoration struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Nodes   []string          `json:"nodes,omitempty"`
	Regions []string          `json:"regions,omitempty"`
	Default bool              `json:"default,omitempty"`
	Images  map[string]string `json:"images,omitempty"`
	Vars    map[string]string `json:"vars,omitempty"`
}

// Reserved are package IDs rejected by all versions of the shared registry.
// Probe itself rejects its compiled-in shadcn ID; CC retains its old releases
// for older Probes. Glass is distributed as a regular package.
var Reserved = []string{}

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	hexColor    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	safePath    = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	contentType = map[string]string{
		".css":         "text/css; charset=utf-8",
		".html":        "text/html; charset=utf-8",
		".js":          "text/javascript; charset=utf-8",
		".mjs":         "text/javascript; charset=utf-8",
		".svg":         "image/svg+xml",
		".wasm":        "application/wasm",
		".ico":         "image/x-icon",
		".webmanifest": "application/manifest+json",
		".ttf":         "font/ttf",
		".otf":         "font/otf",
		".json":        "application/json",
		".webp":        "image/webp",
		".png":         "image/png",
		".jpg":         "image/jpeg",
		".jpeg":        "image/jpeg",
		".gif":         "image/gif",
		".avif":        "image/avif",
		".woff2":       "font/woff2",
		".woff":        "font/woff",
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

// ThemeFileAllowed keeps executable assets exclusive to isolated frontend themes.
// A legacy CSS skin or add-on retains its original passive-file boundary.
func ThemeFileAllowed(m Manifest, name string) bool {
	if _, ok := ContentType(name); !ok {
		return false
	}
	if m.Frontend != nil || m.SettingsUI != nil {
		return true
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".js", ".mjs", ".svg", ".wasm", ".webmanifest":
		return false
	case ".json":
		return name == "theme.json"
	}
	return true
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
	if m.Frontend != nil {
		if _, ok := files[m.Frontend.Entry]; !ok {
			return Manifest{}, nil, errors.New("frontend.entry 不在压缩包里")
		}
	}
	if ui := m.SettingsUI; ui != nil {
		if _, ok := files[ui.Entry]; !ok {
			return Manifest{}, nil, errors.New("settings_ui.entry 不在压缩包里")
		}
	}
	for name := range files {
		if !ThemeFileAllowed(m, name) {
			return Manifest{}, nil, fmt.Errorf("%q：CSS 主题和附加包不收脚本、HTML、SVG 或额外 JSON；独立界面需声明 frontend 或 settings_ui", name)
		}
	}
	for _, p := range m.Styles {
		if _, ok := files[p]; !ok {
			return Manifest{}, nil, fmt.Errorf("styles 里的 %q 不在压缩包里", p)
		}
	}
	for _, d := range m.Decorations {
		for name, p := range d.Images {
			if _, ok := files[p]; !ok {
				return Manifest{}, nil, fmt.Errorf("装饰项 %s 的 images.%s 引用的 %q 不在压缩包里", d.ID, name, p)
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
	case m.Type != TypeSkin && m.Type != TypeAddon:
		return Manifest{}, errors.New(`type 须为 "skin"（主题）或 "addon"（附加包）`)
	case !idPattern.MatchString(m.ID):
		return Manifest{}, errors.New("id 须为 2–32 位小写字母、数字或连字符，以字母开头")
	case slices.Contains(Reserved, m.ID):
		return Manifest{}, fmt.Errorf("id %q 是内置主题，换一个名字", m.ID)
	case m.Name == "" || utf8.RuneCountInString(m.Name) > 30:
		return Manifest{}, errors.New("name 必填，最多 30 个字")
	case utf8.RuneCountInString(m.Hint) > 60, utf8.RuneCountInString(m.Version) > 20, utf8.RuneCountInString(m.Author) > 40:
		return Manifest{}, errors.New("hint 最多 60 字，version 最多 20 字，author 最多 40 字")
	}
	if err := checkDecorations(m.Decorations); err != nil {
		return Manifest{}, err
	}
	if m.Type == TypeAddon {
		switch {
		case !idPattern.MatchString(m.For) || m.For == m.ID:
			return Manifest{}, errors.New("附加包要用 for 写明它依附的主题 id（不能是自己）")
		case len(m.Styles) > 0 || len(m.Settings) > 0 || m.Base != "" || m.Layout != nil || m.Frontend != nil || m.SettingsUI != nil:
			return Manifest{}, errors.New("附加包不带 styles / settings / base / layout / frontend：外观由它依附的主题决定")
		case len(m.Decorations) == 0:
			return Manifest{}, errors.New("附加包至少要有一个装饰项（decorations）")
		}
		return m, nil
	}
	switch {
	case m.For != "":
		return Manifest{}, errors.New(`for 只有附加包（"type": "addon"）能写`)
	case !hexColor.MatchString(m.Chrome.Light) || !hexColor.MatchString(m.Chrome.Dark):
		return Manifest{}, errors.New("chrome.light / chrome.dark 须为 #rrggbb 颜色")
	case len(m.Swatch) != 3:
		return Manifest{}, errors.New("swatch 须为 3 个 #rrggbb 颜色")
	case (m.Frontend == nil && len(m.Styles) == 0) || len(m.Styles) > maxStyles:
		return Manifest{}, fmt.Errorf("styles 须列出 1–%d 个样式表", maxStyles)
	}
	if f := m.Frontend; f != nil {
		if !SafePath(f.Entry) || path.Ext(f.Entry) != ".html" || f.APIVersion != 1 {
			return Manifest{}, errors.New("frontend.entry 须为包内 .html 路径，api_version 须为 1")
		}
		if m.Base != "" || m.Layout != nil {
			return Manifest{}, errors.New("独立前台自己定义布局，不带 base / layout")
		}
	}
	if ui := m.SettingsUI; ui != nil {
		if !SafePath(ui.Entry) || path.Ext(ui.Entry) != ".html" || ui.APIVersion != 1 {
			return Manifest{}, errors.New("settings_ui.entry 须为包内 .html 路径，api_version 须为 1")
		}
	}
	if err := checkSkinSettings(m.Settings); err != nil {
		return Manifest{}, err
	}
	if m.Base != "" && m.Base != BaseGlass && m.Base != BasePlain {
		return Manifest{}, fmt.Errorf(`base 须为 "glass"（液态玻璃，默认）或 "plain"（只有结构、不带玻璃视觉），不是 %q`, m.Base)
	}
	if err := checkLayout(m.Layout); err != nil {
		return Manifest{}, err
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

var (
	decoID   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	decoName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	// A var value lands in a custom property through CSSOM: plain lengths,
	// numbers, colours and times, no quotes, semicolons or braces.
	decoValue = regexp.MustCompile(`^[#A-Za-z0-9.,%()/+ -]{1,64}$`)
)

// checkDecorations validates a package's decorations and normalizes text.
func checkDecorations(list []Decoration) error {
	if len(list) > maxDecorations {
		return fmt.Errorf("decorations 最多 %d 项", maxDecorations)
	}
	seen := map[string]bool{}
	for i := range list {
		d := &list[i]
		d.Name = strings.TrimSpace(d.Name)
		if !decoID.MatchString(d.ID) {
			return fmt.Errorf("装饰项 id %q 须为 1–32 位小写字母、数字或连字符，以字母开头", d.ID)
		}
		if seen[d.ID] {
			return fmt.Errorf("装饰项 id %q 重复", d.ID)
		}
		seen[d.ID] = true
		label := "装饰项 " + d.ID
		if d.Name == "" || utf8.RuneCountInString(d.Name) > 30 {
			return errors.New(label + "：name 必填，最多 30 个字")
		}
		if len(d.Regions) > 30 || len(d.Nodes) > 50 {
			return errors.New(label + "：regions 最多 30 个、nodes 最多 50 个")
		}
		for j, r := range d.Regions {
			d.Regions[j] = strings.TrimSpace(r)
			if d.Regions[j] == "" || len(d.Regions[j]) > 60 {
				return errors.New(label + "：regions 里每一项须为 1–60 字节的地区名")
			}
		}
		for j, n := range d.Nodes {
			d.Nodes[j] = strings.TrimSpace(n)
			if d.Nodes[j] == "" || len(d.Nodes[j]) > 100 {
				return errors.New(label + "：nodes 里每一项须为 1–100 字节的节点名")
			}
		}
		if len(d.Images) > maxDecoImages || len(d.Vars) > maxDecoVars {
			return fmt.Errorf("%s：images 最多 %d 张、vars 最多 %d 个", label, maxDecoImages, maxDecoVars)
		}
		for name, p := range d.Images {
			ext := strings.ToLower(path.Ext(p))
			if !decoName.MatchString(name) {
				return fmt.Errorf("%s：images 的名字 %q 须为 1–24 位小写字母、数字或连字符，以字母开头", label, name)
			}
			if !safePath.MatchString(p) || strings.Contains(p, "..") || (ext != ".webp" && ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".avif") {
				return fmt.Errorf("%s：images.%s 须为包内的 webp / png / jpg / avif 图片相对路径", label, name)
			}
		}
		for name, v := range d.Vars {
			if !decoName.MatchString(name) {
				return fmt.Errorf("%s：vars 的名字 %q 须为 1–24 位小写字母、数字或连字符，以字母开头", label, name)
			}
			if _, clash := d.Images[name]; clash {
				return fmt.Errorf("%s：%q 同时出现在 images 与 vars 里", label, name)
			}
			if !decoValue.MatchString(v) {
				return fmt.Errorf("%s：vars.%s 的值 %q 只能是长度、数字、颜色或时间（字母、数字、# . , %% ( ) / + - 和空格，最多 64 个字符）", label, name, v)
			}
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
			return nil, fmt.Errorf("不支持的文件类型 %q：只收主题静态资源（HTML、JS、CSS、JSON、SVG、WASM、图片和字体）", it.name)
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
