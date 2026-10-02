package unlock

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// TestLive runs the real checks against the host's own egress. It is skipped
// unless UNLOCK_LIVE is set, so the normal test run stays offline.
func TestLive(t *testing.T) {
	if os.Getenv("UNLOCK_LIVE") == "" {
		t.Skip("set UNLOCK_LIVE=1 to run the real egress checks")
	}
	family := os.Getenv("UNLOCK_LIVE_FAMILY")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	results := Run(ctx, Options{Family: family, Timeout: 20 * time.Second, Concurrency: 8})
	for _, r := range results {
		fmt.Printf("%-16s | %-14s | %-24s | %s\n", r.Service, r.Status, r.Region, r.Detail)
	}
	unlocked := 0
	for _, r := range results {
		if r.Unlocked() {
			unlocked++
		}
	}
	fmt.Printf("SCORE %d/%d family=%q\n", unlocked, len(model.UnlockCatalog), family)
}
