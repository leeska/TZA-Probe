package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Source is an external subscription merged into users' subscriptions. The
// Server fetches it every RefreshEvery and keeps the nodes it understood.
type Source struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	// Prefix goes before every node name (e.g. "机场A"), so clients tell
	// them apart from the panel's own inbounds.
	Prefix string `json:"prefix"`
	// UserIDs limits who gets the nodes; empty is every user.
	UserIDs   []string  `json:"user_ids"`
	Nodes     []ExtNode `json:"nodes,omitempty"`
	Skipped   int       `json:"skipped"`
	FetchedAt int64     `json:"fetched_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	Created   int64     `json:"created_at"`
}

// RefreshEvery is how often sources are fetched again.
const RefreshEvery = 6 * time.Hour

// MaxSourceBody bounds a fetched subscription.
const MaxSourceBody = 5 << 20

func (s *Source) Validate() error {
	s.Name, s.URL, s.Prefix = strings.TrimSpace(s.Name), strings.TrimSpace(s.URL), strings.TrimSpace(s.Prefix)
	if s.Name == "" || utf8.RuneCountInString(s.Name) > 40 {
		return errors.New("名称需要 1–40 个字")
	}
	if utf8.RuneCountInString(s.Prefix) > 20 {
		return errors.New("名称前缀最多 20 个字")
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(s.URL) > 2048 {
		return errors.New("订阅地址须是 http(s) 链接")
	}
	if s.UserIDs == nil {
		s.UserIDs = []string{}
	}
	return nil
}

// FetchSource downloads and parses a subscription. It asks as a Clash Meta
// client, which most providers answer with YAML carrying every protocol.
func FetchSource(ctx context.Context, client *http.Client, rawURL string) ([]ExtNode, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "clash.meta")
	if client == nil {
		client = &http.Client{}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("拉取失败：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, 0, fmt.Errorf("订阅地址返回 %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxSourceBody+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > MaxSourceBody {
		return nil, 0, errors.New("订阅内容超过 5 MB")
	}
	return ParseSubscription(body)
}

// rename gives n a new name in every form.
func rename(n ExtNode, name string) ExtNode {
	if name == n.Name {
		return n
	}
	n.Clash = maps.Clone(n.Clash)
	n.Clash["name"] = name
	if n.SingBox != nil {
		n.SingBox = maps.Clone(n.SingBox)
		n.SingBox["tag"] = name
	}
	if n.Link != "" {
		if strings.HasPrefix(n.Link, "vmess://") {
			n.Link = clashToLink(n.Clash)
		} else if i := strings.LastIndex(n.Link, "#"); i >= 0 {
			n.Link = n.Link[:i] + "#" + url.PathEscape(name)
		} else {
			n.Link += "#" + url.PathEscape(name)
		}
	}
	n.Name = name
	return n
}

// Extra is the external nodes member gets: from every enabled source that
// includes them, with the source's prefix.
func Extra(sources []Source, member string) []ExtNode {
	out := []ExtNode{}
	for _, s := range sources {
		if !s.Enabled || (len(s.UserIDs) > 0 && !slices.Contains(s.UserIDs, member)) {
			continue
		}
		for _, n := range s.Nodes {
			if s.Prefix != "" {
				n = rename(n, s.Prefix+" "+n.Name)
			}
			out = append(out, n)
		}
	}
	return out
}

// RefreshSource fetches a source now and stores the result (the old nodes
// stay when the fetch fails).
func (m *Module) RefreshSource(ctx context.Context, id string) (Source, error) {
	src, err := m.Store.Source(ctx, id)
	if err != nil {
		return Source{}, err
	}
	nodes, skipped, ferr := FetchSource(ctx, m.SourceClient, src.URL)
	return m.Store.UpdateSource(ctx, id, func(s *Source) {
		s.FetchedAt = time.Now().Unix()
		if ferr != nil {
			s.Error = ferr.Error()
			return
		}
		s.Nodes, s.Skipped, s.Error = nodes, skipped, ""
	})
}

// RunSources refreshes the enabled sources every RefreshEvery.
func (m *Module) RunSources(ctx context.Context) {
	if m.Store == nil {
		return
	}
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		if list, err := m.Store.Sources(ctx); err == nil {
			for _, s := range list {
				if s.Enabled && time.Since(time.Unix(s.FetchedAt, 0)) >= RefreshEvery {
					if _, err := m.RefreshSource(ctx, s.ID); err != nil {
						slog.Warn("proxy: source refresh", "source", s.Name, "error", err)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
