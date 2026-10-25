package memorystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// clock is a settable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func mustName(t *testing.T, declaration string, parts ...string) string {
	t.Helper()
	name, err := Name(declaration, parts...)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// TestInProcessKeys: a counter counts and expires a TTL after the
// increment that created it, later increments keeping that expiry; a
// claim is set once until it expires or is deleted; Count of an absent
// key is 0.
func TestInProcessKeys(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	s := NewInProcess("shop-cache", WithClock(c.Now))
	defer s.Close()

	views := NewKey("views", mustName(t, "views", "p1"), time.Minute)
	if n, err := views.Count(ctx, s); err != nil || n != 0 {
		t.Fatalf("Count of an absent key = %d, %v", n, err)
	}
	for want := int64(1); want <= 3; want++ {
		n, err := views.Increment(ctx, s)
		if err != nil || n != want {
			t.Fatalf("Increment = %d, %v, want %d", n, err, want)
		}
		c.Add(10 * time.Second)
	}
	if ttl, ok := s.TTL(views.Ref()); !ok || ttl != 30*time.Second {
		t.Errorf("TTL after three increments 10s apart = %s, %v; want 30s, the first increment's expiry", ttl, ok)
	}
	c.Add(30 * time.Second)
	if n, _ := views.Count(ctx, s); n != 0 {
		t.Errorf("Count after the TTL = %d, want 0", n)
	}
	if n, _ := views.Increment(ctx, s); n != 1 {
		t.Errorf("Increment after the TTL = %d, want 1", n)
	}

	receipt := NewKey("receipt-sent", mustName(t, "receipt-sent", "o1"), 10*time.Minute)
	if receipt.TTL() != 10*time.Minute {
		t.Errorf("TTL = %s", receipt.TTL())
	}
	got := []bool{}
	for range 3 {
		ok, err := receipt.Claim(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ok)
	}
	if fmt.Sprint(got) != "[true false false]" {
		t.Errorf("three claims = %v, want one true", got)
	}
	if err := receipt.Delete(ctx, s); err != nil {
		t.Fatal(err)
	}
	if ok, _ := receipt.Claim(ctx, s); !ok {
		t.Error("a claim after Delete is false")
	}
	c.Add(10 * time.Minute)
	if ok, _ := receipt.Claim(ctx, s); !ok {
		t.Error("a claim after the TTL is false")
	}
	if err := (Key{}).Delete(ctx, s); err == nil {
		t.Error("a key For did not build was taken")
	}
	if _, err := s.Increment(ctx, views.Ref(), 0); err == nil {
		t.Error("a TTL of 0 was taken")
	}
}

// TestKeyTTL: a call's TTL is from 1ms to the declaration's maximum.
func TestKeyTTL(t *testing.T) {
	if _, err := KeyTTL("views", 0, time.Hour); err == nil {
		t.Error("0 was taken")
	}
	if _, err := KeyTTL("views", time.Hour+time.Millisecond, time.Hour); err == nil {
		t.Error("over the maximum was taken")
	}
	if ttl, err := KeyTTL("views", 1500*time.Microsecond, time.Hour); err != nil || ttl != time.Millisecond {
		t.Errorf("KeyTTL = %s, %v", ttl, err)
	}
}

type stockLevel struct {
	ProductID string `json:"productId"`
	OnHand    int64  `json:"onHand"`
}

// TestChannels: a typed channel's message reaches every local handler of
// it, in order; a signal is `{}` and any message decodes to it; a message
// that does not decode is dropped and counted; a closed subscription
// hears nothing more; a message over 64 KiB is refused.
func TestChannels(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	s := NewInProcess("shop-cache", WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	defer s.Close()

	samples := NewChannel[stockLevel]("stock-samples", mustName(t, "stock-samples", "p1"))
	got := make(chan string, 10)
	first, err := samples.Subscribe(ctx, s, func(_ context.Context, m stockLevel) { got <- "a:" + fmt.Sprint(m.OnHand) })
	if err != nil {
		t.Fatal(err)
	}
	second, err := samples.Subscribe(ctx, s, func(_ context.Context, m stockLevel) { got <- "b:" + fmt.Sprint(m.OnHand) })
	if err != nil {
		t.Fatal(err)
	}
	if err := samples.Publish(ctx, s, stockLevel{ProductID: "p1", OnHand: 4}); err != nil {
		t.Fatal(err)
	}
	if a, b := recv(t, got), recv(t, got); a != "a:4" || b != "b:4" {
		t.Errorf("handlers heard %s, %s", a, b)
	}
	// A newer release's field is ignored; a message that is no object is
	// dropped and counted.
	if err := s.Publish(ctx, samples.Ref(), []byte(`{"productId":"p1","onHand":5,"bins":[{"bin":"a","extra":{"x":1}}]}`)); err != nil {
		t.Fatal(err)
	}
	if a, b := recv(t, got), recv(t, got); a != "a:5" || b != "b:5" {
		t.Errorf("handlers heard %s, %s", a, b)
	}
	if err := s.Publish(ctx, samples.Ref(), []byte(`"not an object"`)); err != nil {
		t.Fatal(err)
	}
	// Each of the two handlers drops it.
	waitFor(t, func() bool { return counterValue(t, reader, MetricDecodeFailure, "stock-samples", "") == 2 })
	_ = first.Close()
	_ = first.Close()
	if err := samples.Publish(ctx, s, stockLevel{OnHand: 6}); err != nil {
		t.Fatal(err)
	}
	if b := recv(t, got); b != "b:6" {
		t.Errorf("after the first closed, heard %s", b)
	}
	_ = second.Close()

	changed := NewChannel[Signal]("stock-changed", mustName(t, "stock-changed", "p1"))
	woke := make(chan struct{}, 2)
	sub, err := changed.Subscribe(ctx, s, func(context.Context, Signal) { woke <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var raw []byte
	rawSub, _ := s.Subscribe(ctx, changed.Ref(), func(_ context.Context, b []byte) { raw = b; woke <- struct{}{} })
	if err := changed.Publish(ctx, s, Signal{}); err != nil {
		t.Fatal(err)
	}
	<-woke
	<-woke
	_ = rawSub.Close()
	if string(raw) != "{}" {
		t.Errorf("a signal is %q, want {}", raw)
	}
	if err := s.Publish(ctx, changed.Ref(), []byte(`{"any":"thing"}`)); err != nil {
		t.Fatal(err)
	}
	<-woke

	big := make([]byte, MaxMessageBytes+1)
	if err := s.Publish(ctx, samples.Ref(), big); err == nil || errors.Is(err, ErrUnavailable) {
		t.Errorf("a message over 64 KiB: %v", err)
	}

	if n := counterValue(t, reader, MetricPublish, "stock-samples", OutcomeTooLarge); n != 1 {
		t.Errorf("too_large publishes = %d", n)
	}
	if n := counterValue(t, reader, MetricPublish, "stock-samples", OutcomeOK); n != 4 {
		t.Errorf("ok publishes = %d, want 4", n)
	}
	if n := counterValue(t, reader, MetricReceive, "stock-changed", ""); n != 2 {
		t.Errorf("stock-changed receives = %d, want 2, each message once however many handlers it woke", n)
	}
	assertNoPart(t, reader, "p1")
}

// TestUnavailable: a store that is down, Disabled or closed refuses every
// call at once with ErrUnavailable, but a store that is down holds a
// subscription, which hears the store's messages once it is up again;
// Disabled records no metric.
func TestUnavailable(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	down := NewInProcess("shop-cache", WithMeterProvider(provider))
	down.SetAvailable(false)
	closed := NewInProcess("closed")
	_ = closed.Close()
	key := NewKey("views", mustName(t, "views", "p"), time.Minute)
	ch := NewChannel[Signal]("stock-changed", mustName(t, "stock-changed", "p"))
	heard := make(chan struct{}, 1)
	var held Subscription
	for name, s := range map[string]Store{"down": down, "disabled": Disabled("shop-cache"), "closed": closed} {
		t.Run(name, func(t *testing.T) {
			if s.Available() {
				t.Error("Available")
			}
			calls := map[string]error{}
			_, calls["Increment"] = key.Increment(ctx, s)
			_, calls["Count"] = key.Count(ctx, s)
			_, calls["Claim"] = key.Claim(ctx, s)
			calls["Delete"] = key.Delete(ctx, s)
			calls["Publish"] = ch.Publish(ctx, s, Signal{})
			sub, err := ch.Subscribe(ctx, s, func(context.Context, Signal) { heard <- struct{}{} })
			if name == "down" {
				if err != nil {
					t.Errorf("Subscribe while down = %v, want a held subscription", err)
				}
				held = sub
			} else {
				calls["Subscribe"] = err
			}
			for call, err := range calls {
				if !errors.Is(err, ErrUnavailable) {
					t.Errorf("%s = %v, want ErrUnavailable", call, err)
				}
			}
		})
	}
	if n := counterValue(t, reader, MetricKey, "views", OutcomeUnavailable); n != 4 {
		t.Errorf("unavailable key operations of the store that is down = %d, want 4", n)
	}
	if !IsDisabled(Disabled("x")) || IsDisabled(down) {
		t.Error("IsDisabled")
	}
	if Disabled("shop-cache").Name() != "shop-cache" || Disabled("x").Close() != nil {
		t.Error("Disabled's Name or Close")
	}
	down.SetAvailable(true)
	if _, err := key.Increment(ctx, down); err != nil {
		t.Errorf("after SetAvailable(true): %v", err)
	}
	if held == nil {
		return
	}
	defer held.Close()
	if err := ch.Publish(ctx, down, Signal{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-heard:
	case <-time.After(5 * time.Second):
		t.Error("the subscription made while the store was down heard nothing once it was up")
	}
}

// TestConnectionGauge: the gauge is 1 while the store is up, 0 while it is
// down, by store, and gone after Close.
func TestConnectionGauge(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	s := NewInProcess("shop-cache@pr-12", WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	gauge := func() (int64, bool) {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != MetricConnectionUp {
					continue
				}
				for _, p := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					if v, _ := p.Attributes.Value(AttrStore); v.AsString() == "shop-cache@pr-12" {
						return p.Value, true
					}
				}
			}
		}
		return 0, false
	}
	if v, ok := gauge(); !ok || v != 1 {
		t.Errorf("up: %d %v", v, ok)
	}
	s.SetAvailable(false)
	if v, ok := gauge(); !ok || v != 0 {
		t.Errorf("down: %d %v", v, ok)
	}
	_ = s.Close()
	if _, ok := gauge(); ok {
		t.Error("the gauge is observed after Close")
	}
}

// TestWatch: Start pings once; a failing server backs off and comes back;
// MarkDown takes the store down at once and wakes a ping; each change is
// reported once.
func TestWatch(t *testing.T) {
	var up atomic.Bool
	var pings atomic.Int32
	changes := make(chan bool, 10)
	w := NewWatch(func(context.Context) error {
		pings.Add(1)
		if up.Load() {
			return nil
		}
		return errors.New("dial tcp: connection refused")
	}, func(u bool, _ error) { changes <- u }, Timings{Interval: 20 * time.Millisecond, MinBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond})
	defer w.Stop()
	if w.Start(context.Background()) || w.Available() {
		t.Fatal("a server that refuses is up")
	}
	waitFor(t, func() bool { return pings.Load() >= 3 })
	up.Store(true)
	if !<-changes {
		t.Fatal("no change to up")
	}
	if !w.Available() {
		t.Error("not available once the server answers")
	}
	up.Store(false)
	w.MarkDown(errors.New("read: connection reset"))
	if w.Available() || <-changes {
		t.Error("MarkDown did not take the store down")
	}
	w.Stop()
	if w.Available() {
		t.Error("available after Stop")
	}
}

// TestDefaultTimings: the defaults the contract names.
func TestDefaultTimings(t *testing.T) {
	d := Timings{}.OrDefault()
	if d.Ping != time.Second || d.Op != 500*time.Millisecond || d.Interval != 2*time.Second || d.MinBackoff != 100*time.Millisecond || d.MaxBackoff != 5*time.Second {
		t.Errorf("defaults = %+v", d)
	}
	for range 100 {
		if j := Jitter(100 * time.Millisecond); j < 80*time.Millisecond || j > 120*time.Millisecond {
			t.Fatalf("Jitter(100ms) = %s", j)
		}
	}
	if NextBackoff(4*time.Second, 5*time.Second) != 5*time.Second {
		t.Error("NextBackoff passed its maximum")
	}
}

// TestFanoutPanics: a handler that panics is logged and the others still
// run.
func TestFanoutPanics(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	f := NewFanout(zap.New(core))
	var ran []string
	_, first := f.Add("s:c", "c", func(context.Context, []byte) { panic("boom") })
	id, second := f.Add("s:c", "c", func(context.Context, []byte) { ran = append(ran, "second") })
	if !first || second {
		t.Errorf("first = %v, second = %v", first, second)
	}
	if decl, ok := f.Deliver(context.Background(), "s:c", nil); !ok || decl != "c" {
		t.Errorf("Deliver = %q, %v", decl, ok)
	}
	if len(ran) != 1 || logs.FilterMessageSnippet("panicked").Len() != 1 {
		t.Errorf("ran %v, logs %v", ran, logs.All())
	}
	if f.Remove("s:c", id) {
		t.Error("the last handler's Remove while one remains")
	}
	if fmt.Sprint(f.Channels()) != "[s:c]" {
		t.Errorf("Channels = %v", f.Channels())
	}
}

// TestMetadataTokens: the token comes from the metadata server with its
// header, is held until five minutes before it expires, then fetched
// again; a refusal holds no token.
func TestMetadataTokens(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MetadataTokenPath || r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		n := calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("token-%d", n), "expires_in": 3600, "token_type": "Bearer"})
	}))
	defer server.Close()
	var seen []string
	tokens := NewMetadataTokens(server.Client(), strings.TrimPrefix(server.URL, "http://"), func(s string) { seen = append(seen, s) })
	c := newClock()
	tokens.SetClock(c.Now)
	ctx := context.Background()
	user, password, err := tokens.Credentials(ctx)
	if err != nil || user != "default" || password != "token-1" {
		t.Fatalf("Credentials = %q, %q, %v", user, password, err)
	}
	c.Add(54 * time.Minute)
	if token, _ := tokens.Token(ctx); token != "token-1" {
		t.Errorf("54 minutes in: %s, want the held token", token)
	}
	c.Add(2 * time.Minute)
	if token, _ := tokens.Token(ctx); token != "token-2" {
		t.Errorf("56 minutes in: %s, want a new token", token)
	}
	if fmt.Sprint(seen) != "[token-1 token-2]" {
		t.Errorf("onToken saw %v", seen)
	}

	t.Setenv(MetadataHostVariable, strings.TrimPrefix(server.URL, "http://"))
	refused := NewMetadataTokens(&http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		r.Header.Del("Metadata-Flavor")
		return http.DefaultTransport.RoundTrip(r)
	})}, "", nil)
	if _, err := refused.Token(ctx); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a refusal: %v", err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRedactingLog: a client's line reaches the newest open store's
// logger at Debug without its URL or a secret; with no store open it is
// dropped.
func TestRedactingLog(t *testing.T) {
	r := &RedactingLog{}
	core, logs := observer.New(zap.DebugLevel)
	older, newer := zap.New(core).With(zap.String("store", "a")), zap.New(core).With(zap.String("store", "b"))
	releaseOlder := r.Add(older)
	releaseNewer := r.Add(newer)
	r.AddSecret("short")
	r.AddSecret("short-and-longer")
	r.Printf(context.Background(), "redis: failed to dial %s with %s and %s", "rediss://default:pw@10.1.2.3:6379/0", "short-and-longer", "short")
	entries := logs.All()
	if len(entries) != 1 || entries[0].Level != zap.DebugLevel {
		t.Fatalf("entries = %v", entries)
	}
	if got := entries[0].Message; got != "redis: failed to dial [redacted] with [redacted] and [redacted]" {
		t.Errorf("line = %q", got)
	}
	if entries[0].ContextMap()["store"] != "b" {
		t.Errorf("the line went to %v, want the newest store's logger", entries[0].ContextMap())
	}
	releaseNewer()
	releaseOlder()
	r.Printf(context.Background(), "dropped")
	if logs.Len() != 1 {
		t.Error("a line with no store open was written")
	}
	for i := range maxSecrets + 5 {
		r.AddSecret(fmt.Sprintf("token-%02d", i))
	}
	if len(r.secrets) != maxSecrets || r.Redact("token-36") != Redacted {
		t.Errorf("secrets kept: %d", len(r.secrets))
	}
	if RedactError(errors.New("dial redis://127.0.0.1:1/0: refused")) != "dial [redacted] refused" {
		t.Errorf("RedactError = %q", RedactError(errors.New("dial redis://127.0.0.1:1/0: refused")))
	}
}

// TestSharedRateLimit: two limiters, two replicas of one route, over one
// store spend one budget; a refusal is not counted; while the store is
// down, closed or Disabled, Take returns ErrUnavailable at once, for the
// route's in-process limiter to decide.
func TestSharedRateLimit(t *testing.T) {
	store := NewInProcess("shop-cache")
	defer store.Close()
	ctx := context.Background()
	a := NewRateLimiter(store, "shop-api", "product.getProduct", 3, time.Minute)
	b := NewRateLimiter(store, "shop-api", "product.getProduct", 3, time.Minute)
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	var got []string
	for _, l := range []*RateLimiter{a, b, a, b, a} {
		d, err := l.Take(ctx, "203.0.113.7", now)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%v/%d/%d", d.Allowed, d.Remaining, d.RetryAfterSeconds))
	}
	if want := "[true/2/0 true/1/0 true/0/0 false/0/60 false/0/60]"; fmt.Sprint(got) != want {
		t.Fatalf("five requests over two replicas = %v, want %s", got, want)
	}
	ref, _ := RateLimitRef("shop-api", "product.getProduct", now.Truncate(time.Minute).Unix(), "203.0.113.7")
	if n, _ := store.Count(ctx, ref); n != 3 {
		t.Errorf("the window's count = %d, want the 3 allowed", n)
	}
	// Another client has its own budget.
	if d, err := a.Take(ctx, "203.0.113.8", now); err != nil || !d.Allowed {
		t.Errorf("another client = %+v, %v", d, err)
	}
	store.SetAvailable(false)
	if _, err := a.Take(ctx, "203.0.113.7", now); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Take while the store is down = %v", err)
	}
	for _, s := range []Store{Disabled("shop-cache"), nil} {
		if _, err := NewRateLimiter(s, "shop-api", "product.getProduct", 3, time.Minute).Take(ctx, "203.0.113.7", now); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Take over %v = %v", s, err)
		}
	}
}

// TestCounts: Counts reads a MultiCounter's counters in one call, and any
// other store's by a Count each, all at once.
func TestCounts(t *testing.T) {
	store := NewInProcess("shop-cache")
	defer store.Close()
	ctx := context.Background()
	a, b := Ref{Declaration: "views", Name: "views:a"}, Ref{Declaration: "views", Name: "views:b"}
	for range 2 {
		if _, err := store.Increment(ctx, b, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	slow := &slowStore{Store: store, delay: 50 * time.Millisecond}
	for name, s := range map[string]Store{"multi": store, "count each": slow} {
		start := time.Now()
		counts, err := Counts(ctx, s, a, b)
		if err != nil || fmt.Sprint(counts) != "[0 2]" {
			t.Errorf("%s: Counts = %v, %v", name, counts, err)
		}
		if name == "count each" && time.Since(start) > 90*time.Millisecond {
			t.Errorf("two slow Counts took %s, not one round trip", time.Since(start))
		}
	}
	store.SetAvailable(false)
	if _, err := Counts(ctx, slow, a, b); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Counts while down = %v", err)
	}
}

// slowStore is a store whose every key call takes delay, as a round trip
// to a server does. It is no MultiCounter.
type slowStore struct {
	Store
	delay time.Duration
}

func (s *slowStore) Count(ctx context.Context, ref Ref) (int64, error) {
	time.Sleep(s.delay)
	return s.Store.Count(ctx, ref)
}

func (s *slowStore) Increment(ctx context.Context, ref Ref, ttl time.Duration) (int64, error) {
	time.Sleep(s.delay)
	return s.Store.Increment(ctx, ref, ttl)
}

func recv(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no message in 5s")
		return ""
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition did not hold in 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// counterValue is the sum of the counter name's points of declaration,
// and of outcome when it is set.
func counterValue(t *testing.T, reader *sdkmetric.ManualReader, name, declaration, outcome string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				d, _ := p.Attributes.Value(AttrDeclaration)
				o, _ := p.Attributes.Value(AttrOutcome)
				if d.AsString() == declaration && (outcome == "" || o.AsString() == outcome) {
					total += p.Value
				}
			}
		}
	}
	return total
}

// assertNoPart fails when an attribute of any metric holds part.
func assertNoPart(t *testing.T, reader *sdkmetric.ManualReader, part string) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	check := func(set attribute.Set) {
		for _, kv := range set.ToSlice() {
			if strings.Contains(kv.Value.Emit(), part) {
				t.Errorf("attribute %s holds a part: %s", kv.Key, kv.Value.Emit())
			}
		}
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					check(p.Attributes)
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					check(p.Attributes)
				}
			}
		}
	}
}

type compactID [2]byte

func (c compactID) MarshalJSON() ([]byte, error) { return json.Marshal(fmt.Sprintf("c%x", c[:])) }

type email string

type region string

type count int64

// TestParts: each format's wire string; a string-format scalar is its
// JSON string, as the API's wire carries it.
func TestParts(t *testing.T) {
	for got, want := range map[string]string{
		StringPart("a label"):                "a label",
		StringPart(email("ada@example.com")): "ada@example.com",
		StringPart(compactID{1, 2}):          "c0102",
		IntegerPart(count(-42)):              "-42",
		IntegerPart(int32(7)):                "7",
		BooleanPart(true):                    "true",
		EnumPart(region("north")):            "north",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
