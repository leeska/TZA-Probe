package httpapi

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLoginBudgetBoundsConcurrentGuesses(t *testing.T) {
	a := &API{}
	r := httptest.NewRequest("POST", "/api/v1/login", nil)
	now := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for range sharedLoginLimit * 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := a.reserveLogin(r, now)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				accepted++
			}
		}()
	}
	wg.Wait()
	if accepted != sharedLoginLimit {
		t.Fatalf("admitted %d simultaneous guesses; want %d", accepted, sharedLoginLimit)
	}
	// The reservation survives a failure-window reset; it cannot refill a burst.
	a.mu.Lock()
	a.loginWindow = now.Add(-time.Minute)
	a.mu.Unlock()
	if _, ok := a.reserveLogin(r, now); ok {
		t.Fatal("failure-window reset bypassed attempt budget")
	}
	// One new attempt becomes available per refill interval.
	interval := time.Minute / time.Duration(sharedLoginLimit)
	for i := 1; i <= sharedLoginLimit; i++ {
		at := now.Add(time.Duration(i) * interval)
		if delay, ok := a.reserveLogin(r, at); !ok || delay != 0 {
			t.Fatalf("refill %d: delay %s, allowed %v", i, delay, ok)
		}
		if _, ok := a.reserveLogin(r, at); ok {
			t.Fatalf("refill %d allowed another guess", i)
		}
	}
}

func TestLoginBudgetCancellationDoesNotVerify(t *testing.T) {
	a := &API{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/api/v1/login", nil).WithContext(ctx)
	if _, ok := a.reserveLogin(r, time.Now()); ok {
		t.Fatal("cancelled request admitted")
	}
	if len(a.loginNext) != 0 {
		t.Fatal("cancelled request reserved a slot")
	}
}

func TestDeniedLoginBudgetReturnsRefillDelay(t *testing.T) {
	a := &API{}
	r := httptest.NewRequest("POST", "/api/v1/login", nil)
	now := time.Now()
	for range sharedLoginLimit {
		if _, ok := a.reserveLogin(r, now); !ok {
			t.Fatal("fresh burst refused")
		}
	}
	interval := time.Minute / time.Duration(sharedLoginLimit)
	for _, elapsed := range []time.Duration{0, interval / 2, interval - time.Nanosecond} {
		retry, ok := a.reserveLogin(r, now.Add(elapsed))
		if ok || retry != interval-elapsed {
			t.Fatalf("after %s: allowed %v, retry %s", elapsed, ok, retry)
		}
	}
	// Denials do not consume the next refill or prolong the cooldown.
	if retry, ok := a.reserveLogin(r, now.Add(interval)); !ok || retry != 0 {
		t.Fatalf("refill unavailable: %v %s", ok, retry)
	}
}
