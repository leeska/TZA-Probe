// Command browser-e2e is a live acceptance check for the browser worker.
//
// It serves the Server's worker socket on top of a real ipguard.Database
// (enqueue, claim, lease, Complete with the Server's own judgement) and relays
// CONNECT streams through an upstream HTTP proxy instead of a node's Agent.
// Run the worker in a separate, network-less sandbox pointed at the socket:
//
//	browser-e2e -socket /shared/control.sock -expected-ip $(curl -s https://api64.ipify.org) -country HK
//	tza-server browser-worker -socket /shared/control.sock -state /tmp/state   # in --network none
//
// It enqueues one Google and one Meta baseline, waits for both and prints the
// stored jobs as JSON; the exit status is 0 when both completed with a reading.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

func main() {
	socket := flag.String("socket", "", "worker socket to serve")
	expected := flag.String("expected-ip", "", "the exit address the worker must see (api64.ipify.org through the upstream)")
	country := flag.String("country", "HK", "target region")
	services := flag.String("services", "google,meta", "services to check, in order")
	wait := flag.Duration("wait", 6*time.Minute, "give up after")
	flag.Parse()
	upstream, _ := url.Parse(os.Getenv("HTTPS_PROXY"))
	dir, _ := os.MkdirTemp("", "browser-e2e-")
	defer os.RemoveAll(dir)
	store, err := ipguard.Open(filepath.Join(dir, "ipguard.db"))
	if err != nil {
		log.Fatal(err)
	}
	env := ipguard.Environments[*country]
	policy := ipguard.Policy{Revision: 0, Enabled: true, ExpectedIP: *expected, Country: *country, Locale: env.Locale, Timezone: env.Timezone, Services: []string{"google", "meta"}, TimeoutSeconds: 120, NetworkBytes: 20 << 20, IntervalMinutes: 720, DwellSeconds: 8}
	if _, err = store.SavePolicy(context.Background(), "node-e2e", policy); err != nil {
		log.Fatal(err)
	}
	os.Remove(*socket)
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatal(err)
	}
	os.Chmod(*socket, 0666)
	go http.Serve(listener, handler(store, upstream))
	log.Printf("serving %s, upstream %v, expecting exit %s, target %s", *socket, upstream, *expected, *country)

	deadline := time.Now().Add(*wait)
	ok := true
	for _, service := range splitComma(*services) {
		job, err := store.Enqueue(context.Background(), "node-e2e", service, "baseline", "")
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("enqueued %s %s", service, job.ID)
		for {
			jobs, _ := store.Jobs(context.Background(), "node-e2e")
			var current *ipguard.Job
			for i := range jobs {
				if jobs[i].ID == job.ID {
					current = &jobs[i]
				}
			}
			if current != nil && !ipguard.Active(current.State) {
				out, _ := json.MarshalIndent(current, "", "  ")
				fmt.Println(string(out))
				ok = ok && current.State == "completed" && current.Result != nil && current.Result.Page.Value != ""
				break
			}
			if time.Now().After(deadline) {
				log.Printf("timed out waiting for %s", job.ID)
				os.Exit(1)
			}
			time.Sleep(time.Second)
		}
	}
	if !ok {
		os.Exit(1)
	}
}

func splitComma(value string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(value); i++ {
		if i == len(value) || value[i] == ',' {
			if i > start {
				out = append(out, value[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func handler(store *ipguard.Database, upstream *url.URL) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /claim", func(w http.ResponseWriter, r *http.Request) {
		job, err := store.Claim(r.Context())
		if err != nil {
			http.Error(w, "claim failed", 500)
			return
		}
		json.NewEncoder(w).Encode(job)
	})
	mux.HandleFunc("GET /jobs/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		if _, err := store.Running(r.Context(), r.PathValue("id"), r.Header.Get("X-Job-Lease")); err != nil {
			http.Error(w, "inactive", 409)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /jobs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
		decoder.DisallowUnknownFields() // the Server's readJSON is just as strict
		var result ipguard.Result
		if err := decoder.Decode(&result); err != nil {
			log.Printf("result rejected: %v", err)
			http.Error(w, "invalid result", 400)
			return
		}
		if err := store.Complete(r.Context(), r.PathValue("id"), r.Header.Get("X-Job-Lease"), result); err != nil {
			http.Error(w, "job no longer valid", 409)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("CONNECT /connect/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, err := store.Running(r.Context(), r.PathValue("id"), r.Header.Get("X-Job-Lease"))
		host := r.Header.Get("X-Destination")
		if err != nil || !ipguard.PermittedHost(job.Service, host) {
			http.Error(w, "inactive", 409)
			return
		}
		remote, err := dial(upstream, net.JoinHostPort(host, "443"))
		if err != nil {
			http.Error(w, "destination unavailable", 502)
			return
		}
		defer remote.Close()
		local, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer local.Close()
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		done := make(chan struct{}, 2)
		go func() { io.Copy(remote, buffer); done <- struct{}{} }()
		go func() { io.Copy(local, remote); done <- struct{}{} }()
		<-done
	})
	return mux
}

func dial(upstream *url.URL, target string) (net.Conn, error) {
	if upstream == nil || upstream.Host == "" {
		return net.DialTimeout("tcp", target, 10*time.Second)
	}
	conn, err := net.DialTimeout("tcp", upstream.Host, 10*time.Second)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 200 {
		conn.Close()
		return nil, fmt.Errorf("upstream CONNECT %s failed", target)
	}
	return conn, nil
}
