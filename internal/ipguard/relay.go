package ipguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Offer struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
	Job    Job    `json:"job"`
	Host   string `json:"host"`
}

type Status struct {
	AllowedIPs []string `json:"allowed_ips"`
	Enabled    bool     `json:"enabled"`
	Reason     string   `json:"reason"`
	ReceivedAt int64    `json:"received_at"`
}

type pending struct {
	offer     Offer
	connected chan *websocket.Conn
	done      chan struct{}
}
type relayBudget struct{ used atomic.Int64 }

type Broker struct {
	Store Store
	// WorkerConfigured is set when the Server listens on a worker socket
	// (--browser-socket): the browser is installed, whether or not it is
	// connected right now.
	WorkerConfigured bool
	mutex            sync.Mutex
	pending          map[string]*pending
	statuses         map[string]Status
	budgets          map[string]*relayBudget
	streams          map[string]int
	workerSeen       atomic.Int64
}

func NewBroker(store Store) *Broker {
	return &Broker{Store: store, pending: map[string]*pending{}, statuses: map[string]Status{}, budgets: map[string]*relayBudget{}, streams: map[string]int{}}
}
func (broker *Broker) WorkerSeen() int64 { return broker.workerSeen.Load() }
func (broker *Broker) Status(node string) Status {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	status := broker.statuses[node]
	status.AllowedIPs = slices.Clone(status.AllowedIPs)
	return status
}
func (broker *Broker) Report(node string, status Status) {
	addresses := []string{}
	if len(status.AllowedIPs) <= 32 {
		for _, candidate := range status.AllowedIPs {
			address, err := netip.ParseAddr(candidate)
			if err == nil && PublicAddress(address) && address.String() == candidate && !slices.Contains(addresses, candidate) {
				addresses = append(addresses, candidate)
			}
		}
	}
	slices.Sort(addresses)
	status.AllowedIPs = addresses
	status.ReceivedAt = time.Now().Unix()
	if len(status.Reason) > 200 {
		status.Reason = "local_policy_error"
	}
	broker.mutex.Lock()
	broker.statuses[node] = status
	broker.mutex.Unlock()
}

func (broker *Broker) Poll(ctx context.Context, node string) (*Offer, error) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		broker.mutex.Lock()
		var offer *Offer
		for _, entry := range broker.pending {
			if entry.offer.Job.NodeID == node {
				value := entry.offer
				offer = &value
				break
			}
		}
		broker.mutex.Unlock()
		if offer != nil {
			return offer, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		case <-ticker.C:
		}
	}
}

func (broker *Broker) Accept(w http.ResponseWriter, r *http.Request, node, id, secret string) error {
	http.NewResponseController(w).SetReadDeadline(time.Time{})
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	broker.mutex.Lock()
	entry := broker.pending[id]
	if entry == nil || entry.offer.Job.NodeID != node || entry.offer.Secret != secret {
		broker.mutex.Unlock()
		return errors.New("unknown stream")
	}
	delete(broker.pending, id)
	broker.mutex.Unlock()
	if _, err := broker.Store.Running(r.Context(), entry.offer.Job.ID, entry.offer.Job.Lease); err != nil {
		return err
	}
	connection, err := (&websocket.Upgrader{ReadBufferSize: 32768, WriteBufferSize: 32768, CheckOrigin: func(request *http.Request) bool { return request.Header.Get("Origin") == "" }}).Upgrade(w, r, nil)
	if err != nil {
		return nil
	}
	connection.SetReadLimit(32768)
	select {
	case entry.connected <- connection:
		return nil
	case <-entry.done:
		connection.Close()
		return nil
	case <-r.Context().Done():
		connection.Close()
		return nil
	}
}

func (broker *Broker) Dial(ctx context.Context, job Job, host string) (*websocket.Conn, error) {
	if !PermittedHost(job.Service, host) {
		return nil, errors.New("origin_not_allowed")
	}
	status := broker.Status(job.NodeID)
	if !status.Enabled || time.Now().Unix()-status.ReceivedAt > 45 {
		return nil, errors.New("出口 Agent 离线或未获本机许可")
	}
	entry := &pending{offer: Offer{ID: ID(), Secret: ID(), Job: job, Host: host}, connected: make(chan *websocket.Conn), done: make(chan struct{})}
	broker.mutex.Lock()
	for id := range broker.budgets {
		if id != job.ID && broker.streams[id] == 0 {
			delete(broker.budgets, id)
		}
	}
	if broker.streams[job.ID] >= 16 {
		broker.mutex.Unlock()
		return nil, errors.New("连接数量超限")
	}
	broker.streams[job.ID]++
	broker.pending[entry.offer.ID] = entry
	broker.mutex.Unlock()
	connected := false
	defer func() {
		close(entry.done)
		broker.mutex.Lock()
		delete(broker.pending, entry.offer.ID)
		if !connected {
			broker.streams[job.ID]--
		}
		broker.mutex.Unlock()
	}()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case connection := <-entry.connected:
		connected = true
		return connection, nil
	case <-timer.C:
		return nil, errors.New("出口连接超时")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (broker *Broker) Bridge(ctx context.Context, job Job, local net.Conn, remote *websocket.Conn) int64 {
	broker.mutex.Lock()
	budget := broker.budgets[job.ID]
	if budget == nil {
		budget = &relayBudget{}
		broker.budgets[job.ID] = budget
	}
	broker.mutex.Unlock()
	defer broker.release(job.ID)
	defer local.Close()
	defer remote.Close()
	deadline := time.Unix(job.StartedAt, 0).Add(time.Duration(job.Policy.TimeoutSeconds) * time.Second)
	local.SetDeadline(deadline)
	remote.SetReadDeadline(deadline)
	remote.SetWriteDeadline(deadline)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				local.Close()
				remote.Close()
				return
			case <-ticker.C:
				if _, err := broker.Store.Running(context.Background(), job.ID, job.Lease); err != nil {
					local.Close()
					remote.Close()
					return
				}
			}
		}
	}()
	results := make(chan error, 2)
	go func() {
		buffer := make([]byte, 32768)
		for {
			count, err := local.Read(buffer)
			if count > 0 {
				if budget.used.Add(int64(count)) > job.Policy.NetworkBytes {
					results <- errors.New("network_budget")
					return
				}
				if writeErr := remote.WriteMessage(websocket.BinaryMessage, buffer[:count]); writeErr != nil {
					results <- writeErr
					return
				}
			}
			if err != nil {
				results <- err
				return
			}
		}
	}()
	go func() {
		for {
			kind, message, err := remote.ReadMessage()
			if err != nil {
				results <- err
				return
			}
			if kind != websocket.BinaryMessage {
				results <- errors.New("invalid stream frame")
				return
			}
			if budget.used.Add(int64(len(message))) > job.Policy.NetworkBytes {
				results <- errors.New("network_budget")
				return
			}
			if _, err = local.Write(message); err != nil {
				results <- err
				return
			}
		}
	}()
	<-results
	local.Close()
	remote.Close()
	<-results
	return budget.used.Load()
}

func (broker *Broker) release(id string) {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	broker.streams[id]--
	if broker.streams[id] <= 0 {
		delete(broker.streams, id)
	}
}

func (broker *Broker) Usage(id string) int64 {
	broker.mutex.Lock()
	defer broker.mutex.Unlock()
	if budget := broker.budgets[id]; budget != nil {
		return budget.used.Load()
	}
	return 0
}
func (broker *Broker) Forget(id string) {
	broker.mutex.Lock()
	delete(broker.budgets, id)
	broker.mutex.Unlock()
}

func (broker *Broker) WorkerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /claim", func(w http.ResponseWriter, r *http.Request) {
		broker.workerSeen.Store(time.Now().Unix())
		job, err := broker.Store.Claim(r.Context())
		if err != nil {
			http.Error(w, "claim failed", 500)
			return
		}
		writeJSON(w, job)
	})
	mux.HandleFunc("POST /jobs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		broker.workerSeen.Store(time.Now().Unix())
		var result Result
		if err := readJSON(w, r, &result); err != nil {
			http.Error(w, "invalid result", 400)
			return
		}
		result.NetworkBytes = broker.Usage(r.PathValue("id"))
		err := broker.Store.Complete(r.Context(), r.PathValue("id"), r.Header.Get("X-Job-Lease"), result)
		if err != nil {
			http.Error(w, "job no longer valid", 409)
			return
		}
		broker.Forget(r.PathValue("id"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /jobs/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		broker.workerSeen.Store(time.Now().Unix())
		if _, err := broker.Store.Running(r.Context(), r.PathValue("id"), r.Header.Get("X-Job-Lease")); err != nil {
			http.Error(w, "inactive", 409)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("CONNECT /connect/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, err := broker.Store.Running(r.Context(), r.PathValue("id"), r.Header.Get("X-Job-Lease"))
		if err != nil {
			http.Error(w, "inactive", 409)
			return
		}
		remote, err := broker.Dial(r.Context(), *job, r.Header.Get("X-Destination"))
		if err != nil {
			http.Error(w, "destination unavailable", 502)
			return
		}
		local, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			remote.Close()
			broker.release(job.ID)
			return
		}
		if buffer.Reader.Buffered() > 0 {
			local.Close()
			remote.Close()
			broker.release(job.ID)
			return
		}
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		broker.Bridge(context.Background(), *job, local, remote)
	})
	return mux
}
