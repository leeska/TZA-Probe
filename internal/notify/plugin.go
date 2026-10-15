package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// Plugins finds an installed notify plugin by id (internal/plugin.Store).
type Plugins interface {
	Plugin(id string) (tzapkg.Plugin, error)
}

// pluginLimit bounds event.text, in UTF-16 code units: far below the 64 KB
// request body limit even after JSON escaping.
const pluginLimit = 8000

// pluginResponseLimit caps how much of a plugin channel's answer is read.
const pluginResponseLimit = 64 << 10

// EventVars are the event.* template variables of docs/PLUGINS.md.
func EventVars(e model.Event) map[string]string {
	status, priority, urgent, active := "告警解除", "3", "", "false"
	switch {
	case e.Kind == "test":
		status, priority, active = "连接测试", "2", "true"
	case e.Active:
		status, priority, urgent, active = "告警触发", "4", "1", "true"
	}
	title := []string{"TZA Probe", status}
	if e.Kind != "test" {
		title = append(title, kindName(e.Kind))
	}
	if e.NodeName != "" {
		title = append(title, singleLine(e.NodeName))
	}
	id, timestamp := "", ""
	if e.ID > 0 {
		id = strconv.FormatInt(e.ID, 10)
	}
	if e.At > 0 {
		timestamp = strconv.FormatInt(e.At, 10)
	}
	return map[string]string{
		"title":     strings.Join(title, " · "),
		"text":      messageText(e, "", pluginLimit),
		"message":   e.Message,
		"status":    status,
		"kind":      e.Kind,
		"kind_name": kindName(e.Kind),
		"node":      singleLine(e.NodeName),
		"node_id":   e.NodeID,
		"time":      eventTime(e.At),
		"timestamp": timestamp,
		"id":        id,
		"active":    active,
		"urgent":    urgent,
		"priority":  priority,
	}
}

// sendPlugin delivers through an installed notify plugin. Every error names
// the plugin and the reason in plain words; none carries the request URL,
// headers or the raw answer (they may hold the channel's secrets), only what
// the plugin's own success rule reports.
func (s Transport) sendPlugin(ctx context.Context, c Config, e model.Event) error {
	if s.Plugins == nil {
		return errors.New("这个 Server 没有启用插件，通知插件无法使用")
	}
	p, err := s.Plugins.Plugin(c.Plugin)
	if err != nil {
		return fmt.Errorf("通知插件 %s 不可用（%v），请在「告警与通知 → 配置出口」重新选择渠道", c.Plugin, err)
	}
	if p.Kind != tzapkg.KindNotify {
		return fmt.Errorf("插件 %s 不是通知插件，请重新选择通知渠道", c.Plugin)
	}
	settings, err := p.CheckSettings(c.PluginSettings)
	if err != nil {
		return fmt.Errorf("%s 的设置不再有效（%v），请重新保存通知渠道", p.Name, err)
	}
	req, err := p.RenderNotify(settings, EventVars(e), time.Now())
	if err != nil {
		return fmt.Errorf("%s：%v", p.Name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return fmt.Errorf("%s 的请求创建失败", p.Name)
	}
	hr.Header = req.Headers
	if hr.Header.Get("User-Agent") == "" {
		hr.Header.Set("User-Agent", "TZA-Probe/"+model.Version)
	}
	client := http.Client{Timeout: 5 * time.Second, Transport: s.RoundTripper, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(hr)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s 连接超时（5 秒）", p.Name)
		}
		return fmt.Errorf("%s 连接失败，请检查服务器网络与插件设置", p.Name)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, pluginResponseLimit))
	return p.CheckResponse(resp.StatusCode, answer)
}
