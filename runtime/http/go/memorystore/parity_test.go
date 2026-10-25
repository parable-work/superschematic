package memorystore

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// parityPath is the vector file the Go, TypeScript and Rust memory store
// runtimes read (D75), relative to this package.
const parityPath = "../../testdata/memorystore_parity.json"

type parityFile struct {
	Names []struct {
		Name        string   `json:"name"`
		Declaration string   `json:"declaration"`
		Parts       []string `json:"parts"`
		Store       string   `json:"store"`
		Member      string   `json:"member"`
		Want        *string  `json:"want"`
		FullName    *string  `json:"fullName"`
	} `json:"names"`
	RateLimit struct {
		Windows []struct {
			Name      string `json:"name"`
			Limit     int    `json:"limit"`
			WindowMs  int64  `json:"windowMs"`
			NowMs     int64  `json:"nowMs"`
			PrevCount int    `json:"prevCount"`
			CurrCount int    `json:"currCount"`
			Want      struct {
				Allowed           bool    `json:"allowed"`
				Estimate          float64 `json:"estimate"`
				CurrWindowStart   int64   `json:"currWindowStart"`
				PrevWindowStart   int64   `json:"prevWindowStart"`
				IncrementTTLMs    int64   `json:"incrementTtlMs"`
				RetryAfterSeconds int64   `json:"retryAfterSeconds"`
			} `json:"want"`
		} `json:"windows"`
		Keys []struct {
			Name        string  `json:"name"`
			Service     string  `json:"service"`
			Operation   string  `json:"operation"`
			WindowStart int64   `json:"windowStart"`
			ClientKey   string  `json:"clientKey"`
			Want        *string `json:"want"`
		} `json:"keys"`
	} `json:"rateLimit"`
}

func readParity(t *testing.T) parityFile {
	t.Helper()
	data, err := os.ReadFile(parityPath)
	if err != nil {
		t.Fatal(err)
	}
	var f parityFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Names) == 0 || len(f.RateLimit.Windows) == 0 || len(f.RateLimit.Keys) == 0 {
		t.Fatal("the parity file has an empty section")
	}
	return f
}

// TestNameParity: Name and Prefix build every name of the shared vectors,
// and refuse every one the vectors refuse.
func TestNameParity(t *testing.T) {
	for _, v := range readParity(t).Names {
		t.Run(v.Name, func(t *testing.T) {
			name, err := Name(v.Declaration, v.Parts...)
			var prefix string
			if err == nil && v.Store != "" {
				prefix, err = Prefix(v.Store, v.Member)
			}
			if v.Want == nil {
				if err == nil {
					t.Fatalf("built %q, want a refusal", name)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v, want %q", err, *v.Want)
			}
			if name != *v.Want {
				t.Errorf("Name = %q, want %q", name, *v.Want)
			}
			if v.FullName != nil {
				if got := FullName(prefix, Ref{Declaration: v.Declaration, Name: name}); got != *v.FullName {
					t.Errorf("FullName = %q, want %q", got, *v.FullName)
				}
			}
		})
	}
}

// TestRateLimitKeyParity: RateLimitRef names each window's counter as the
// shared vectors do, the client's key hashed, and refuses a service or an
// operation that is no segment.
func TestRateLimitKeyParity(t *testing.T) {
	for _, v := range readParity(t).RateLimit.Keys {
		t.Run(v.Name, func(t *testing.T) {
			ref, err := RateLimitRef(v.Service, v.Operation, v.WindowStart, v.ClientKey)
			if v.Want == nil {
				if err == nil {
					t.Fatalf("built %q, want a refusal", ref.Name)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ref.Name != *v.Want || ref.Declaration != RateLimitDeclaration {
				t.Errorf("ref = %+v, want %q", ref, *v.Want)
			}
		})
	}
}

// TestRateLimitWindowParity: for each window of the shared vectors, the
// counter over an in-process store holding the vector's counts gives
// httprate the counts of the vector's windows, httprate's rule decides as
// the vector does, and an allowed request's increment creates the
// current window's key with the vector's TTL. The rule here is httprate's
// own (limiter.go: calculateRate, then OnLimit's rate+1 > limit), which
// the counter plugs into unchanged.
func TestRateLimitWindowParity(t *testing.T) {
	const service, operation, client = "shop-api", "ProductQueries.getProduct", "203.0.113.7"
	for _, v := range readParity(t).RateLimit.Windows {
		t.Run(v.Name, func(t *testing.T) {
			now := time.UnixMilli(v.NowMs).UTC()
			store := NewInProcess("shop-cache", WithClock(func() time.Time { return now }))
			defer store.Close()
			window := time.Duration(v.WindowMs) * time.Millisecond
			curr := now.Truncate(window)
			prev := curr.Add(-window)
			if curr.Unix() != v.Want.CurrWindowStart || prev.Unix() != v.Want.PrevWindowStart {
				t.Fatalf("windows %d and %d, want %d and %d", curr.Unix(), prev.Unix(), v.Want.CurrWindowStart, v.Want.PrevWindowStart)
			}
			ctx := context.Background()
			for start, n := range map[time.Time]int{curr: v.CurrCount, prev: v.PrevCount} {
				ref, err := RateLimitRef(service, operation, start.Unix(), client)
				if err != nil {
					t.Fatal(err)
				}
				for range n {
					if _, err := store.Increment(ctx, ref, RateLimitTTL(window)); err != nil {
						t.Fatal(err)
					}
				}
			}
			counter := NewRateLimitCounter(store, service, operation)
			counter.Config(v.Limit, window)
			currCount, prevCount, err := counter.Get(client, curr, prev)
			if err != nil {
				t.Fatal(err)
			}
			if currCount != v.CurrCount || prevCount != v.PrevCount {
				t.Fatalf("Get = %d, %d, want %d, %d", currCount, prevCount, v.CurrCount, v.PrevCount)
			}
			diff := now.Sub(curr)
			estimate := float64(prevCount)*(float64(window)-float64(diff))/float64(window) + float64(currCount)
			if math.Abs(estimate-v.Want.Estimate) > 1e-9 {
				t.Errorf("estimate = %v, want %v", estimate, v.Want.Estimate)
			}
			allowed := int(math.Round(estimate))+1 <= v.Limit
			if allowed != v.Want.Allowed {
				t.Fatalf("allowed = %v, want %v", allowed, v.Want.Allowed)
			}
			if !allowed {
				if got := int64(window.Seconds()); got != v.Want.RetryAfterSeconds {
					t.Errorf("Retry-After = %d, want %d", got, v.Want.RetryAfterSeconds)
				}
				return
			}
			fresh := NewInProcess("shop-cache", WithClock(func() time.Time { return now }))
			defer fresh.Close()
			c := NewRateLimitCounter(fresh, service, operation)
			c.Config(v.Limit, window)
			if err := c.Increment(client, curr); err != nil {
				t.Fatal(err)
			}
			ref, _ := RateLimitRef(service, operation, curr.Unix(), client)
			if ttl, ok := fresh.TTL(ref); !ok || ttl.Milliseconds() != v.Want.IncrementTTLMs {
				t.Errorf("the increment's TTL = %s (%v), want %dms", ttl, ok, v.Want.IncrementTTLMs)
			}
		})
	}
}
