// Package agent orchestrates injected collector, transport and probe adapters.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

type Collector interface {
	Collect(context.Context) (model.Metrics, error)
}
type Prober interface {
	Run(context.Context, model.Job) model.Result
}
type Transport interface {
	Report(context.Context, model.Metrics) error
	Jobs(context.Context) ([]model.Job, error)
	Finish(context.Context, model.Result) error
}
type Agent struct {
	Collector Collector
	Prober    Prober
	Transport Transport
}

func (a Agent) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			m, err := a.Collector.Collect(ctx)
			if err == nil {
				err = a.Transport.Report(ctx, m)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("metrics report failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer wg.Done()
		// Every leased job runs on its own and the loop keeps polling meanwhile:
		// a minutes-long unlock round or a minute-long trace must not stall the
		// latency probes. The Server bounds what is in flight (one lease per kind
		// at a time), so the Agent runs whatever it is handed.
		var workers sync.WaitGroup
		defer workers.Wait()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			jobs, err := a.Transport.Jobs(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Warn("job polling failed", "error", err)
			}
			for _, j := range jobs {
				workers.Add(1)
				go func(job model.Job) {
					defer workers.Done()
					a.runJob(ctx, job)
				}(j)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	wg.Wait()
}

// pollInterval is how often the Agent asks for jobs.
var pollInterval = 10 * time.Second

// runJob executes one job and uploads its result, retrying the upload up to
// three times.
func (a Agent) runJob(ctx context.Context, job model.Job) {
	result := a.Prober.Run(ctx, job)
	for attempt := 0; attempt < 3; attempt++ {
		if err := a.Transport.Finish(ctx, result); err == nil {
			return
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if ctx.Err() == nil {
		slog.Warn("result upload failed", "job", job.ID)
	}
}

type HTTPTransport struct {
	endpoint, token string
	client          *http.Client
}

func NewTransport(endpoint, token string, allowHTTP bool) (*HTTPTransport, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("endpoint must be an absolute server origin without credentials or path")
	}
	if len(token) != 64 {
		return nil, errors.New("agent token must have 64 characters")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && (allowHTTP || loopback)) {
		return nil, errors.New("HTTPS required; allow_http is only for a trusted private network")
	}
	return &HTTPTransport{endpoint: strings.TrimSuffix(endpoint, "/"), token: token, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (t *HTTPTransport) request(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.endpoint+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// The cause (DNS, TLS, refused, timeout) is what an operator needs; the
		// error names the endpoint and path only, the token rides in a header.
		return fmt.Errorf("server connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(out)
	}
	return nil
}
func (t *HTTPTransport) Report(ctx context.Context, m model.Metrics) error {
	return t.request(ctx, "POST", "/api/v1/agent/report", model.Report{Protocol: model.Protocol, Metrics: m}, nil)
}
func (t *HTTPTransport) Jobs(ctx context.Context) ([]model.Job, error) {
	var jobs []model.Job
	err := t.request(ctx, "GET", "/api/v1/agent/jobs", nil, &jobs)
	return jobs, err
}
func (t *HTTPTransport) Finish(ctx context.Context, r model.Result) error {
	return t.request(ctx, "POST", "/api/v1/agent/results", r, nil)
}
