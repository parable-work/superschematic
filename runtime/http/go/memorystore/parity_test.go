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

// TestRateLimitWindowParity: for each window of the shared vectors,
// RateLimiter over an in-process store holding the vector's counts reads
// the vector's windows, decides as the vector does, with httprate's
// Retry-After, and an allowed request's increment creates the current
// window's key with the vector's TTL. httprate's own arithmetic
// (limiter.go: calculateRate, then OnLimit's rate+1 > limit), which the
// route's in-process limiter applies, decides the same, so a route answers
// alike with and without its store.
func TestRateLimitWindowParity(t *testing.T) {
	const service, operation, client = "shop-api", "ProductQueries.getProduct", "203.0.113.7"
	for _, v := range readParity(t).RateLimit.Windows {
		t.Run(v.Name, func(t *testing.T) {
			now := time.UnixMilli(v.NowMs).UTC()
			store := NewInProcess("shop-cache", WithClock(func() time.Time { return now }))
			defer store.Close()
			window := time.Duration(v.WindowMs) * time.Millisecond
			curr := time.UnixMilli(v.Want.CurrWindowStart * 1000)
			prev := time.UnixMilli(v.Want.PrevWindowStart * 1000)
			ctx := context.Background()
			refs := map[time.Time]Ref{}
			for start, n := range map[time.Time]int{curr: v.CurrCount, prev: v.PrevCount} {
				ref, err := RateLimitRef(service, operation, start.Unix(), client)
				if err != nil {
					t.Fatal(err)
				}
				refs[start] = ref
				for range n {
					if _, err := store.Increment(ctx, ref, RateLimitTTL(window)); err != nil {
						t.Fatal(err)
					}
				}
			}
			estimate := slidingWindowEstimate(v.WindowMs, now.Sub(curr), int64(v.PrevCount), int64(v.CurrCount))
			if math.Abs(estimate-v.Want.Estimate) > 1e-9 {
				t.Errorf("estimate = %v, want %v", estimate, v.Want.Estimate)
			}
			d, err := NewRateLimiter(store, service, operation, v.Limit, window).Take(ctx, client, now)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != v.Want.Allowed || d.RetryAfterSeconds != v.Want.RetryAfterSeconds {
				t.Fatalf("decision = %+v, want allowed %v, Retry-After %d", d, v.Want.Allowed, v.Want.RetryAfterSeconds)
			}
			if !d.Reset.Equal(curr.Add(window)) || d.Limit != v.Limit {
				t.Errorf("decision = %+v, want the limit %d and the reset %s", d, v.Limit, curr.Add(window))
			}
			// httprate's own arithmetic, in nanoseconds since the window began.
			diff := now.Sub(now.Truncate(window))
			rate := float64(v.PrevCount)*(float64(window)-float64(diff))/float64(window) + float64(v.CurrCount)
			if httprate := int(math.Round(rate))+1 <= v.Limit; httprate != d.Allowed {
				t.Errorf("httprate's rule allows %v, the shared limit %v", httprate, d.Allowed)
			}
			n, _ := store.Count(ctx, refs[curr])
			ttl, _ := store.TTL(refs[curr])
			if !d.Allowed {
				if n != int64(v.CurrCount) {
					t.Errorf("a refused request counted: %d, want %d", n, v.CurrCount)
				}
				return
			}
			if n != int64(v.CurrCount)+1 {
				t.Errorf("the allowed request's count = %d, want %d", n, v.CurrCount+1)
			}
			if ttl.Milliseconds() != v.Want.IncrementTTLMs {
				t.Errorf("the increment's TTL = %s, want %dms", ttl, v.Want.IncrementTTLMs)
			}
		})
	}
}
