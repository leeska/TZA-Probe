package browserworker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

// Permitted is the browser-side allowlist: HTTPS on 443 to the service's origins
// (plus the egress echo). The Agent checks the same list again on its side.
func Permitted(service, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	return ipguard.PermittedHost(service, strings.ToLower(u.Hostname()))
}

// Tunnel is the loopback CONNECT proxy Chromium is pointed at. Its only way out
// is the Server's worker socket, where every stream is bound to the job lease
// and relayed through the node's Agent; there is no direct fallback.
type Tunnel struct {
	Socket  string
	Job     ipguard.Job
	Addr    string
	timeout time.Duration

	listener net.Listener
	mu       sync.Mutex
	conns    map[net.Conn]bool
	wg       sync.WaitGroup
}

func OpenTunnel(socket string, job ipguard.Job) (*Tunnel, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t := &Tunnel{Socket: socket, Job: job, Addr: listener.Addr().String(), listener: listener, conns: map[net.Conn]bool{}, timeout: 15 * time.Second}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.track(conn, true)
			t.wg.Add(1)
			go func() { defer t.wg.Done(); defer t.track(conn, false); t.handle(conn) }()
		}
	}()
	return t, nil
}

func (t *Tunnel) track(conn net.Conn, open bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if open {
		t.conns[conn] = true
	} else {
		delete(t.conns, conn)
		conn.Close()
	}
}

func refuse(conn net.Conn) {
	io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
}

func (t *Tunnel) handle(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReaderSize(conn, 16<<10)
	request, err := http.ReadRequest(reader)
	if err != nil || request.Method != http.MethodConnect || (request.Proto != "HTTP/1.1" && request.Proto != "HTTP/1.0") || !Permitted(t.Job.Service, "https://"+request.Host) {
		refuse(conn)
		return
	}
	host := strings.ToLower(strings.TrimSuffix(request.Host, ":443"))
	if h, _, err := net.SplitHostPort(request.Host); err == nil {
		host = strings.ToLower(h)
	}
	upstream, err := net.DialTimeout("unix", t.Socket, 5*time.Second)
	if err != nil {
		refuse(conn)
		return
	}
	t.track(upstream, true)
	defer t.track(upstream, false)
	upstream.SetDeadline(time.Now().Add(t.timeout))
	fmt.Fprintf(upstream, "CONNECT /connect/%s HTTP/1.1\r\nHost: localhost\r\nX-Job-Lease: %s\r\nX-Destination: %s\r\n\r\n", t.Job.ID, t.Job.Lease, host)
	upReader := bufio.NewReaderSize(upstream, 16<<10)
	response, err := http.ReadResponse(upReader, nil)
	if err != nil || response.StatusCode != http.StatusOK {
		refuse(conn)
		return
	}
	conn.SetDeadline(time.Time{})
	upstream.SetDeadline(time.Time{})
	if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, reader); done <- struct{}{} }()
	go func() { io.Copy(conn, upReader); done <- struct{}{} }()
	<-done
}

// Close stops accepting and cuts every open stream.
func (t *Tunnel) Close() {
	t.listener.Close()
	t.mu.Lock()
	for conn := range t.conns {
		conn.Close()
	}
	t.mu.Unlock()
	t.wg.Wait()
}

// worker socket client --------------------------------------------------------

// Client talks to the Server's private worker socket.
type Client struct {
	Socket string
	http   *http.Client
}

// ProtocolError is a non-2xx answer from the Server.
type ProtocolError struct{ Status int }

func (e *ProtocolError) Error() string { return fmt.Sprintf("worker protocol status %d", e.Status) }

func NewClient(socket string) *Client {
	return &Client{Socket: socket, http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
			DisableKeepAlives: true,
		},
	}}
}

// Call sends method route with an optional JSON body and decodes the answer.
func (c *Client) Call(ctx context.Context, method, route string, body io.Reader, lease string, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://worker"+route, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Job-Lease", lease)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
	if err != nil {
		return err
	}
	if (response.StatusCode != 200 && response.StatusCode != 204) || len(raw) > 256<<10 {
		return &ProtocolError{Status: response.StatusCode}
	}
	if out != nil && len(raw) > 0 {
		return decodeJSON(raw, out)
	}
	return nil
}
