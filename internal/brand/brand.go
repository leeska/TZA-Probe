// Package brand keeps the panel's uploaded site logo: one image in
// <data>/branding/, served at /site-logo. The site name lives in the settings
// table (model.SettingSiteName); only the file needs a place outside the
// database, the same way uploaded theme packages do.
//
// The type is decided by the file's bytes, never by a name or a client header,
// and limited to the raster formats browsers show as icons plus SVG. An SVG
// carrying script, event handlers, external references or entity declarations
// is refused; what is served is additionally locked down by its own CSP, so an
// SVG opened directly (not as an <img>) can run nothing either.
package brand

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MaxLogo caps one uploaded logo. A favicon-sized mark is a few KB; 512 KB
// leaves room for a detailed PNG without letting the panel serve photos.
const MaxLogo = 512 << 10

// Path is where the logo is served.
const Path = "/site-logo"

// formats are the accepted types: file extension on disk and served type.
var formats = []struct{ ext, ctype string }{
	{".png", "image/png"},
	{".jpg", "image/jpeg"},
	{".gif", "image/gif"},
	{".webp", "image/webp"},
	{".ico", "image/x-icon"},
	{".svg", "image/svg+xml"},
}

// ErrInvalid is an upload this store refuses; its message says why.
var ErrInvalid = errors.New("logo 无效")

type invalid string

func (e invalid) Error() string        { return string(e) }
func (e invalid) Is(target error) bool { return target == ErrInvalid }

// Store is the logo on disk. A nil Store, or one without Dir, has no logo.
type Store struct {
	Dir string

	mu     sync.RWMutex
	loaded bool
	cur    *logo
}

type logo struct {
	file, ctype, version string
	modified             time.Time
}

// Detect returns the extension and served type of an accepted image, or an
// error naming what is wrong with it.
func Detect(data []byte) (ext, ctype string, err error) {
	switch {
	case len(data) == 0:
		return "", "", invalid("图片是空的")
	case len(data) > MaxLogo:
		return "", "", invalid(fmt.Sprintf("logo 最大 %d KB", MaxLogo>>10))
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", "image/png", nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg", "image/jpeg", nil
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif", "image/gif", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp", "image/webp", nil
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return ".ico", "image/x-icon", nil
	}
	if err := checkSVG(data); err != nil {
		return "", "", err
	}
	return ".svg", "image/svg+xml", nil
}

// checkSVG accepts a well-formed SVG document with nothing that could run or
// fetch: no script / foreignObject / embedding elements, no on* handlers, no
// link that leaves the document, no DTD (entities), no javascript: anywhere.
func checkSVG(data []byte) error {
	const notImage = "只支持 PNG、JPEG、GIF、WebP、ICO 或 SVG 图片"
	if !bytes.Contains(data, []byte("<svg")) {
		return invalid(notImage)
	}
	if bytes.Contains(bytes.ToLower(data), []byte("javascript:")) {
		return invalid("SVG 里不能有 javascript: 链接")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	root := true
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid("SVG 不是有效的 XML：" + err.Error())
		}
		switch t := tok.(type) {
		case xml.Directive:
			return invalid("SVG 里不能有 DOCTYPE / ENTITY 声明")
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if root {
				if name != "svg" {
					return invalid(notImage)
				}
				root = false
			}
			switch name {
			case "script", "foreignobject", "iframe", "object", "embed", "handler", "listener":
				return invalid("SVG 里不能有 <" + t.Name.Local + "> 元素")
			}
			for _, a := range t.Attr {
				attr := strings.ToLower(a.Name.Local)
				if strings.HasPrefix(attr, "on") {
					return invalid("SVG 里不能有事件属性 " + a.Name.Local)
				}
				if attr == "href" && !strings.HasPrefix(strings.TrimSpace(a.Value), "#") && !strings.HasPrefix(strings.TrimSpace(a.Value), "data:image/") {
					return invalid("SVG 里的链接只能指向文档内部（#id）")
				}
			}
		}
	}
	if root {
		return invalid(notImage)
	}
	return nil
}

// Save replaces the logo with data after checking it.
func (s *Store) Save(data []byte) error {
	if s == nil || s.Dir == "" {
		return errors.New("logo 存储不可用")
	}
	ext, _, err := Detect(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.Dir, "logo"+ext)); err != nil {
		return err
	}
	// 换了格式时旧文件还在：删掉别的格式，只留这一份。
	for _, f := range formats {
		if f.ext != ext {
			os.Remove(filepath.Join(s.Dir, "logo"+f.ext))
		}
	}
	s.loaded = false
	return nil
}

// Remove deletes the logo; the panel falls back to the built-in mark.
func (s *Store) Remove() error {
	if s == nil || s.Dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range formats {
		if err := os.Remove(filepath.Join(s.Dir, "logo"+f.ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	s.loaded = false
	return nil
}

// current reads (once, until the next change) which logo file is on disk.
func (s *Store) current() *logo {
	if s == nil || s.Dir == "" {
		return nil
	}
	s.mu.RLock()
	if s.loaded {
		cur := s.cur
		s.mu.RUnlock()
		return cur
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur, s.loaded = nil, true
	for _, f := range formats {
		file := filepath.Join(s.Dir, "logo"+f.ext)
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		s.cur = &logo{file: file, ctype: f.ctype, version: hex.EncodeToString(sum[:6]), modified: info.ModTime()}
		break
	}
	return s.cur
}

// URL is the logo's address with a content version (so browsers pick up a new
// upload, favicon caches included), or "" when no logo is set.
func (s *Store) URL() string {
	cur := s.current()
	if cur == nil {
		return ""
	}
	return Path + "?v=" + cur.version
}

// ServeHTTP serves the logo, or 404 when none is set.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cur := s.current()
	if cur == nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(cur.file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", cur.ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	// 直接打开 /site-logo 时（不是当 <img> 用），SVG 也什么都跑不了、取不了。
	h.Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox")
	http.ServeContent(w, r, "", cur.modified, f)
}
