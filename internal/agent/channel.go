package agent

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leeska/TZA-Probe/internal/channel"
)

// Channel keeps the management channel (protocol v2) open: it redials with
// backoff and serves the Server's streams with Handlers. Capabilities is what
// it announces (each key of Handlers belongs to one of them).
type Channel struct {
	Transport    *HTTPTransport
	Handlers     map[string]channel.Handler
	Capabilities []string
}

func (c *Channel) Run(ctx context.Context) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := c.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			wait = 2 * time.Second
		}
		slog.Debug("management channel closed", "error", err, "retry", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > time.Minute {
			wait = time.Minute
		}
	}
}

func (c *Channel) once(ctx context.Context) error {
	endpoint, _ := url.Parse(c.Transport.endpoint)
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = channel.Path
	caps := append([]string{}, c.Capabilities...)
	sort.Strings(caps)
	header := http.Header{"Authorization": []string{"Bearer " + c.Transport.token}, "X-Channel-Capabilities": []string{strings.Join(caps, ",")}}
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: nil}).DialContext(ctx, endpoint.String(), header)
	if err != nil {
		return err
	}
	slog.Info("management channel connected", "capabilities", caps)
	return channel.ServeAgent(ctx, conn, c.Handlers)
}
