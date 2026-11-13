package certs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cloudflare is the part of the Cloudflare API DNS-01 needs. The token
// needs Zone:Read and DNS:Edit on the zones it issues for.
type Cloudflare struct {
	Token string
	// BaseURL is the API root (tests point it elsewhere).
	BaseURL string
	Client  *http.Client
}

func (cf *Cloudflare) base() string {
	if cf.BaseURL != "" {
		return cf.BaseURL
	}
	return "https://api.cloudflare.com/client/v4"
}

func (cf *Cloudflare) do(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, cf.base()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cf.Token)
	req.Header.Set("Content-Type", "application/json")
	client := cf.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连不上 Cloudflare：%w", err)
	}
	defer res.Body.Close()
	var envelope struct {
		Success bool                       `json:"success"`
		Errors  []struct{ Message string } `json:"errors"`
		Result  json.RawMessage            `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("Cloudflare 返回 %d，内容无法识别", res.StatusCode)
	}
	if !envelope.Success {
		msgs := []string{}
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		if len(msgs) == 0 {
			msgs = append(msgs, res.Status)
		}
		return errors.New("Cloudflare：" + strings.Join(msgs, "；"))
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

// Verify checks the token is active.
func (cf *Cloudflare) Verify(ctx context.Context) error {
	var r struct{ Status string }
	if err := cf.do(ctx, "GET", "/user/tokens/verify", nil, &r); err != nil {
		return err
	}
	if r.Status != "active" {
		return fmt.Errorf("令牌状态是 %s", r.Status)
	}
	return nil
}

// Zone is a Cloudflare zone with its authoritative name servers.
type Zone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NameServers []string `json:"name_servers"`
}

// Zone finds the zone holding name, trying name and then each parent.
func (cf *Cloudflare) Zone(ctx context.Context, name string) (Zone, error) {
	labels := strings.Split(strings.TrimPrefix(name, "*."), ".")
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		var zones []Zone
		if err := cf.do(ctx, "GET", "/zones?name="+url.QueryEscape(candidate), nil, &zones); err != nil {
			return Zone{}, err
		}
		if len(zones) > 0 {
			return zones[0], nil
		}
	}
	return Zone{}, fmt.Errorf("这个 Cloudflare 令牌下找不到 %s 所在的域名（zone）", name)
}

// AddTXT creates a TXT record and returns its id.
func (cf *Cloudflare) AddTXT(ctx context.Context, zoneID, name, content string) (string, error) {
	var rec struct{ ID string }
	err := cf.do(ctx, "POST", "/zones/"+zoneID+"/dns_records", map[string]any{"type": "TXT", "name": name, "content": content, "ttl": 60}, &rec)
	return rec.ID, err
}

func (cf *Cloudflare) DeleteRecord(ctx context.Context, zoneID, id string) error {
	return cf.do(ctx, "DELETE", "/zones/"+zoneID+"/dns_records/"+id, nil, nil)
}
