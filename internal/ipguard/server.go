package ipguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func (broker *Broker) ServeWorker(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("worker socket must be absolute")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0027 != 0 {
		return fmt.Errorf("worker socket directory must be private and not group-writable")
	}
	if existing, err := os.Lstat(path); err == nil {
		owner, ok := existing.Sys().(*syscall.Stat_t)
		if existing.Mode()&os.ModeSocket == 0 || !ok || int(owner.Uid) != os.Geteuid() {
			return fmt.Errorf("refusing to replace worker socket")
		}
		connection, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			connection.Close()
			return fmt.Errorf("worker socket is in use")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if owner, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(path, -1, int(owner.Gid)); err != nil {
			return err
		}
	}
	if err = os.Chmod(path, 0660); err != nil {
		return err
	}
	server := &http.Server{Handler: broker.WorkerHandler(), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() { <-ctx.Done(); server.Close() }()
	return server.Serve(listener)
}

func (broker *Broker) Schedule(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		broker.ScheduleOnce(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (broker *Broker) ScheduleOnce(ctx context.Context, now time.Time) {
	policies, err := broker.Store.Policies(ctx)
	if err != nil || now.Unix()-broker.WorkerSeen() > 30 {
		return
	}
	for node, policy := range policies {
		status := broker.Status(node)
		if !policy.Enabled || policy.IntervalMinutes == 0 || !status.Enabled || now.Unix()-status.ReceivedAt > 45 {
			continue
		}
		jobs, err := broker.Store.Jobs(ctx, node)
		if err != nil {
			continue
		}
		busy := false
		for _, job := range jobs {
			busy = busy || Active(job.State)
		}
		if busy {
			continue
		}
		chained := false
		for _, service := range policy.Services {
			for _, job := range jobs {
				if job.Service != service || job.Policy.Revision != policy.Revision {
					continue
				}
				if policy.AutoRecover && job.State == "completed" && job.CurrentSignal() && now.Unix()-job.FinishedAt < 1800 {
					phase := ""
					if job.Phase == "baseline" && job.Result.Assessment == "other_region" {
						phase = "experiment"
					}
					if job.Phase == "experiment" && job.Result.Assessment == "experiment_only" {
						phase = "followup"
					}
					if phase != "" {
						if _, err := broker.Store.Enqueue(ctx, node, service, phase, phase+":"+job.ID); err == nil {
							chained = true
						}
					}
				}
				break
			}
			if chained {
				break
			}
		}
		if chained {
			continue
		}
		for _, service := range policy.Services {
			if service != "google" && service != "meta" {
				continue
			}
			due := true
			for _, job := range jobs {
				if job.Service == "google" && job.Result != nil && job.Result.Page.Signal == "homepage_hkredirect" {
					continue
				}
				if job.Service == service && SameEnvironment(job.Policy, policy) && job.CreatedAt > now.Unix()-int64(policy.IntervalMinutes)*60 {
					due = false
					break
				}
			}
			if due {
				slot := now.Unix() / (int64(policy.IntervalMinutes) * 60)
				if _, err := broker.Store.Enqueue(ctx, node, service, "baseline", "country:"+node+":"+service+":"+strconv.FormatInt(policy.Revision, 10)+":"+strconv.FormatInt(slot, 10)); err == nil {
					break
				}
			}
		}
	}
}
