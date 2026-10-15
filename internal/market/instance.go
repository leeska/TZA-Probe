package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/model"
)

// InstanceFile holds the credentials of the bound instance in the data
// directory, 0600 like admin-account. CC only keeps the token's SHA-256.
const InstanceFile = "cc-instance"

// instance is the stored binding. URL is the CC it belongs to: a token is only
// ever sent to that CC, so pointing --cc-url elsewhere cannot leak it.
type instance struct {
	URL     string    `json:"url"`
	ID      string    `json:"instance_id"`
	Token   string    `json:"instance_token"`
	Account string    `json:"account"`
	BoundAt time.Time `json:"bound_at"`
}

// pending is a device-code binding waiting for the administrator to confirm
// on CC. The device code stays on the Server; the browser only sees the user code.
type pending struct {
	deviceCode string
	Pending
	next time.Time // earliest next token request
}

// Pending is what the panel shows while waiting: the code to type on CC.
type Pending struct {
	UserCode  string    `json:"user_code"`
	VerifyURL string    `json:"verify_url"`
	ExpiresAt time.Time `json:"expires_at"`
	Interval  int       `json:"interval"`
}

// Status is the market's state for the panel.
type Status struct {
	Configured  bool       `json:"configured"`
	URL         string     `json:"url,omitempty"`
	Probe       string     `json:"probe,omitempty"`
	TrustedKeys int        `json:"trusted_keys"`
	Bound       bool       `json:"bound"`
	InstanceID  string     `json:"instance_id,omitempty"`
	Account     string     `json:"account,omitempty"`
	BoundAt     *time.Time `json:"bound_at,omitempty"`
	// OtherURL: the stored binding belongs to a different CC than the one
	// configured now; it is not used until disconnected or --cc-url changes back.
	OtherURL string   `json:"other_url,omitempty"`
	Pending  *Pending `json:"pending,omitempty"`
}

// Poll states.
const (
	PollPending = "pending"
	PollBound   = "bound"
	PollExpired = "expired"
	PollNone    = "none"
)

// PollResult is one step of the device flow.
type PollResult struct {
	Status   string `json:"status"`
	Account  string `json:"account,omitempty"`
	Interval int    `json:"interval,omitempty"`
}

func (c *Client) instancePath() string { return filepath.Join(c.dir, InstanceFile) }

// load reads the stored binding; nil when there is none or it is unreadable.
func (c *Client) load() *instance {
	if c.dir == "" {
		return nil
	}
	data, err := os.ReadFile(c.instancePath())
	if err != nil {
		return nil
	}
	var in instance
	if json.Unmarshal(data, &in) != nil || in.Token == "" || in.URL == "" {
		return nil
	}
	return &in
}

// token is the instance token for this CC, or "" when not bound to it.
func (c *Client) token() string {
	if in := c.load(); in != nil && in.URL == c.base.String() {
		return in.Token
	}
	return ""
}

// dropToken forgets the binding after CC answered 401 to its token (revoked,
// or unbound from the CC user center). A binding that changed meanwhile stays.
func (c *Client) dropToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if in := c.load(); in != nil && in.Token == token {
		os.Remove(c.instancePath())
	}
}

// save writes the binding atomically with 0600 permissions, owned like the
// data directory (the same handling as admin-account).
func (c *Client) save(in instance) error {
	if c.dir == "" {
		return errors.New("这个 Server 没有数据目录，无法保存绑定")
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, "."+InstanceFile+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := account.OwnLikeDir(tmp.Name(), c.dir); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.instancePath())
}

// Status reports the configuration, the binding and any pending device flow.
func (c *Client) Status() Status {
	if c == nil {
		return Status{}
	}
	s := Status{Configured: true, URL: c.base.String(), Probe: c.probe, TrustedKeys: len(c.keys)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if in := c.load(); in != nil {
		if in.URL == s.URL {
			at := in.BoundAt
			s.Bound, s.InstanceID, s.Account, s.BoundAt = true, in.ID, in.Account, &at
		} else {
			s.OtherURL = in.URL
		}
	}
	if p := c.pending; p != nil && c.now().Before(p.ExpiresAt) {
		view := p.Pending
		s.Pending = &view
	}
	return s
}

// Connect starts binding this instance to a CC account (device-code flow,
// like `gh auth login`): the administrator opens VerifyURL, signs in and
// enters UserCode; Poll then collects the token. name labels the instance in
// the CC user center ("TZA Probe" when empty); nothing else about the
// Server is sent.
func (c *Client) Connect(ctx context.Context, name string) (Pending, error) {
	if c == nil {
		return Pending{}, ErrNotConfigured
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "TZA Probe"
	}
	if utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Pending{}, model.Invalid("实例名最多 40 个字，不能含控制字符")
	}
	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		VerifyURL  string `json:"verify_url"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	body := map[string]string{"name": name, "probe": c.probe}
	if err := c.call(ctx, http.MethodPost, []string{"device", "code"}, nil, body, &out, false); err != nil {
		return Pending{}, err
	}
	verify, err := url.Parse(out.VerifyURL)
	if out.DeviceCode == "" || len(out.DeviceCode) > 512 || out.UserCode == "" || len(out.UserCode) > 32 || err != nil || (verify.Scheme != "https" && verify.Scheme != "http") || verify.Host == "" {
		return Pending{}, rejected("设备码响应不完整")
	}
	interval := min(max(out.Interval, 2), 60)
	if out.Interval == 0 {
		interval = 5
	}
	expires := min(max(out.ExpiresIn, 60), 3600)
	if out.ExpiresIn == 0 {
		expires = 600
	}
	now := c.now()
	p := &pending{
		deviceCode: out.DeviceCode,
		Pending:    Pending{UserCode: clean(out.UserCode, 32), VerifyURL: verify.String(), ExpiresAt: now.Add(time.Duration(expires) * time.Second).UTC(), Interval: interval},
		next:       now.Add(time.Duration(interval) * time.Second),
	}
	c.mu.Lock()
	c.pending = p
	c.mu.Unlock()
	return p.Pending, nil
}

// Poll asks CC once whether the pending binding was confirmed, no more often
// than the interval CC asked for (earlier calls answer pending without a
// request). On success the credentials are saved and the flow ends.
func (c *Client) Poll(ctx context.Context) (PollResult, error) {
	if c == nil {
		return PollResult{}, ErrNotConfigured
	}
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	c.mu.Lock()
	p := c.pending
	now := c.now()
	switch {
	case p == nil:
		c.mu.Unlock()
		if in := c.load(); in != nil && in.URL == c.base.String() {
			return PollResult{Status: PollBound, Account: in.Account}, nil
		}
		return PollResult{Status: PollNone}, nil
	case !now.Before(p.ExpiresAt):
		c.pending = nil
		c.mu.Unlock()
		return PollResult{Status: PollExpired}, nil
	case now.Before(p.next):
		c.mu.Unlock()
		return PollResult{Status: PollPending, Interval: p.Interval}, nil
	}
	code := p.deviceCode
	c.mu.Unlock()

	var out struct {
		InstanceID    string `json:"instance_id"`
		InstanceToken string `json:"instance_token"`
		Account       struct {
			Name string `json:"name"`
		} `json:"account"`
	}
	err := c.call(ctx, http.MethodPost, []string{"device", "token"}, nil, map[string]string{"device_code": code}, &out, false)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != p {
		// Cancelled or restarted meanwhile: this answer belongs to an old flow.
		return PollResult{Status: PollNone}, nil
	}
	var remote *RemoteError
	switch {
	case errors.As(err, &remote) && remote.Status == http.StatusPreconditionRequired:
		p.next = c.now().Add(time.Duration(p.Interval) * time.Second)
		return PollResult{Status: PollPending, Interval: p.Interval}, nil
	case errors.As(err, &remote) && (remote.Status == http.StatusTooManyRequests || remote.Message == "slow_down"):
		p.Interval = min(p.Interval+5, 60)
		p.next = c.now().Add(time.Duration(p.Interval) * time.Second)
		return PollResult{Status: PollPending, Interval: p.Interval}, nil
	case errors.As(err, &remote) && remote.Status == http.StatusGone:
		c.pending = nil
		return PollResult{Status: PollExpired}, nil
	case errors.As(err, &remote) && remote.Status < 500:
		// Denied or unknown device code: this flow cannot succeed any more.
		c.pending = nil
		return PollResult{}, err
	case err != nil:
		// CC unreachable or failing: keep the flow, the next poll retries.
		p.next = c.now().Add(time.Duration(p.Interval) * time.Second)
		return PollResult{}, err
	}
	if out.InstanceToken == "" || len(out.InstanceToken) > 512 || out.InstanceID == "" || len(out.InstanceID) > 128 {
		c.pending = nil
		return PollResult{}, rejected("绑定响应不完整")
	}
	in := instance{URL: c.base.String(), ID: clean(out.InstanceID, 128), Token: out.InstanceToken, Account: clean(out.Account.Name, 60), BoundAt: c.now().UTC()}
	if err := c.save(in); err != nil {
		return PollResult{}, fmt.Errorf("保存绑定凭据失败：%w", err)
	}
	c.pending = nil
	return PollResult{Status: PollBound, Account: in.Account}, nil
}

// Disconnect cancels a pending binding and unbinds the instance: CC is told
// to revoke the token (only the CC the token belongs to), and the local
// credentials are deleted even when CC cannot be reached, in which case
// warning says so.
func (c *Client) Disconnect(ctx context.Context) (warning string, err error) {
	if c == nil {
		return "", ErrNotConfigured
	}
	c.mu.Lock()
	c.pending = nil
	in := c.load()
	c.mu.Unlock()
	if in == nil {
		return "", nil
	}
	if in.URL == c.base.String() {
		err := c.call(ctx, http.MethodDelete, []string{"instance"}, nil, nil, nil, true)
		var remote *RemoteError
		// 401 / 404: CC already forgot this token, which is the goal.
		if err != nil && !(errors.As(err, &remote) && (remote.Status == http.StatusUnauthorized || remote.Status == http.StatusNotFound)) {
			warning = "TZA-CC 暂时没有确认解绑（" + err.Error() + "）；本机凭据已删除，可在 TZA-CC 用户中心移除这台实例"
		}
	} else {
		warning = "删除的是绑定到另一个 TZA-CC（" + in.URL + "）的凭据，未通知那边；可在它的用户中心移除这台实例"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.Remove(c.instancePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return warning, nil
}
