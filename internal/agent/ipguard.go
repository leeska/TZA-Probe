package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leeska/TZA-Probe/internal/ipguard"
)

type EgressPolicy struct {
	Enabled    bool              `json:"enabled"`
	AllowedIPs []string          `json:"allowed_ips"`
	Services   []string          `json:"services"`
	SourceIPs  map[string]string `json:"source_ips,omitempty"`
}

func (policy EgressPolicy) sourceAddress(expected string) (netip.Addr, error) {
	address, err := netip.ParseAddr(expected)
	if err != nil || !ipguard.PublicAddress(address) || address.String() != expected || !slices.Contains(policy.AllowedIPs, expected) {
		return netip.Addr{}, errors.New("exit address not approved")
	}
	source, mapped := policy.SourceIPs[expected]
	if !mapped {
		return address, nil
	}
	bound, err := netip.ParseAddr(source)
	if err != nil || bound.String() != source || !bound.IsGlobalUnicast() || bound.IsLinkLocalUnicast() || bound.Is4In6() || bound.Is4() != address.Is4() {
		return netip.Addr{}, errors.New("invalid local source address")
	}
	return bound, nil
}

type Egress struct {
	Transport  *HTTPTransport
	PolicyFile string
	mutex      sync.Mutex
	active     map[string]bool
	jobUsage   map[string]egressUsage
}

type egressUsage struct {
	Bytes    int64
	Deadline int64
}

func (egress *Egress) policy() (EgressPolicy, error) {
	var policy EgressPolicy
	info, err := os.Lstat(egress.PolicyFile)
	if err != nil {
		return policy, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return policy, errors.New("egress policy must be a private regular file")
	}
	raw, err := os.ReadFile(egress.PolicyFile)
	if err != nil {
		return policy, err
	}
	if len(raw) > 8192 {
		return policy, errors.New("policy too large")
	}
	if err = json.Unmarshal(raw, &policy); err != nil {
		return policy, err
	}
	if len(policy.SourceIPs) > 32 {
		return policy, errors.New("too many local source addresses")
	}
	for expected := range policy.SourceIPs {
		if _, err := policy.sourceAddress(expected); err != nil {
			return policy, err
		}
	}
	return policy, nil
}

func (egress *Egress) Run(ctx context.Context) {
	var streams sync.WaitGroup
	defer streams.Wait()
	egress.active = map[string]bool{}
	egress.jobUsage = map[string]egressUsage{}
	for ctx.Err() == nil {
		policy, err := egress.policy()
		reason := ""
		if err != nil {
			reason = "local_permission_required"
		}
		report := ipguard.Status{Enabled: err == nil && policy.Enabled, Reason: reason}
		if report.Enabled {
			report.AllowedIPs = policy.AllowedIPs
		}
		cycle, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err = egress.Transport.request(cycle, "POST", "/api/v1/agent/ipguard/status", report, nil); err == nil && report.Enabled {
			var offer *ipguard.Offer
			if err = egress.Transport.request(cycle, "GET", "/api/v1/agent/ipguard/streams", nil, &offer); err == nil && offer != nil {
				egress.mutex.Lock()
				already := egress.active[offer.ID]
				if !already && len(egress.active) < 16 {
					egress.active[offer.ID] = true
				} else {
					already = true
				}
				egress.mutex.Unlock()
				if !already {
					streams.Add(1)
					go func() {
						defer streams.Done()
						defer func() { egress.mutex.Lock(); delete(egress.active, offer.ID); egress.mutex.Unlock() }()
						egress.stream(ctx, *offer)
					}()
				}
			}
		}
		cancel()
		if err != nil || !report.Enabled {
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func resolveEgress(ctx context.Context, host, family string) (netip.Addr, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return netip.Addr{}, errors.New("DNS unavailable")
	}
	var selected netip.Addr
	for _, address := range addresses {
		if !ipguard.PublicAddress(address) {
			return netip.Addr{}, errors.New("non-public DNS address")
		}
		if (family == "ipv4") == address.Is4() {
			selected = address
		}
	}
	if !selected.IsValid() {
		return selected, errors.New("address family unavailable")
	}
	return selected, nil
}

func (egress *Egress) consume(job ipguard.Job, count int) bool {
	egress.mutex.Lock()
	defer egress.mutex.Unlock()
	now := time.Now().Unix()
	policy, err := egress.policy()
	if err != nil || !policy.Enabled || !slices.Contains(policy.Services, job.Service) || !slices.Contains(policy.AllowedIPs, job.Policy.ExpectedIP) || count < 0 {
		return false
	}
	deadline := job.StartedAt + int64(job.Policy.TimeoutSeconds)
	if job.ID == "" || job.StartedAt > now+5 || deadline <= now || job.Policy.TimeoutSeconds < 10 || job.Policy.TimeoutSeconds > 180 || job.Policy.NetworkBytes < 1<<20 || job.Policy.NetworkBytes > 100<<20 {
		return false
	}
	if egress.jobUsage == nil {
		egress.jobUsage = map[string]egressUsage{}
	}
	for id, usage := range egress.jobUsage {
		if usage.Deadline <= now {
			delete(egress.jobUsage, id)
		}
	}
	usage := egress.jobUsage[job.ID]
	if usage.Deadline != 0 && usage.Deadline != deadline || int64(count) > job.Policy.NetworkBytes-usage.Bytes {
		return false
	}
	usage.Bytes += int64(count)
	usage.Deadline = deadline
	egress.jobUsage[job.ID] = usage
	return true
}

func (egress *Egress) stream(parent context.Context, offer ipguard.Offer) {
	policy, err := egress.policy()
	if err != nil || !policy.Enabled || !slices.Contains(policy.Services, offer.Job.Service) || !slices.Contains(policy.AllowedIPs, offer.Job.Policy.ExpectedIP) || !ipguard.PermittedHost(offer.Job.Service, offer.Host) || offer.Job.Policy.Validate() != nil {
		return
	}
	if offer.Job.StartedAt < time.Now().Add(-3*time.Minute).Unix() || offer.Job.StartedAt > time.Now().Add(5*time.Second).Unix() {
		return
	}
	ctx, cancel := context.WithDeadline(parent, time.Unix(offer.Job.StartedAt, 0).Add(time.Duration(offer.Job.Policy.TimeoutSeconds)*time.Second))
	defer cancel()
	if !egress.consume(offer.Job, 0) {
		return
	}
	ip, err := netip.ParseAddr(offer.Job.Policy.ExpectedIP)
	if err != nil {
		return
	}
	source, err := policy.sourceAddress(offer.Job.Policy.ExpectedIP)
	if err != nil {
		return
	}
	family := "ipv6"
	network := "tcp6"
	if ip.Is4() {
		family = "ipv4"
		network = "tcp4"
	}
	address, err := resolveEgress(ctx, offer.Host, family)
	if err != nil {
		return
	}
	endpoint, _ := url.Parse(egress.Transport.endpoint)
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = "/api/v1/agent/ipguard/streams/" + offer.ID
	header := http.Header{"Authorization": []string{"Bearer " + egress.Transport.token}, "X-Stream-Secret": []string{offer.Secret}}
	connection, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: nil}).DialContext(ctx, endpoint.String(), header)
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(32768)
	remote, err := (&net.Dialer{Timeout: 10 * time.Second, LocalAddr: &net.TCPAddr{IP: net.IP(source.AsSlice())}}).DialContext(ctx, network, net.JoinHostPort(address.String(), "443"))
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan error, 2)
	go func() {
		buffer := make([]byte, 32768)
		for {
			count, err := remote.Read(buffer)
			if count > 0 {
				if !egress.consume(offer.Job, count) {
					done <- errors.New("budget exhausted")
					return
				}
				if err := connection.WriteMessage(websocket.BinaryMessage, buffer[:count]); err != nil {
					done <- err
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	go func() {
		for {
			kind, data, err := connection.ReadMessage()
			if err != nil {
				done <- err
				return
			}
			if kind != websocket.BinaryMessage || !egress.consume(offer.Job, len(data)) {
				done <- errors.New("invalid frame or exhausted budget")
				return
			}
			if _, err = remote.Write(data); err != nil {
				done <- err
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-done:
			break wait
		case <-ticker.C:
			if !egress.consume(offer.Job, 0) {
				break wait
			}
		}
	}
	remote.Close()
	connection.Close()
}
