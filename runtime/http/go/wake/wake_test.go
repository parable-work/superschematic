package wake

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fake is a platform's wake and its token endpoint: it counts each and
// records what the wake was sent.
type fake struct {
	mu     sync.Mutex
	tokens int
	wakes  []string
	auth   []string
	status int
	server *httptest.Server
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{status: http.StatusOK}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/token":
			if r.Method != http.MethodGet || r.Header.Get("Metadata-Flavor") != "Fake" {
				http.Error(w, "no flavor", http.StatusForbidden)
				return
			}
			f.tokens++
			_, _ = w.Write([]byte(`{"access_token":"t-1","expires_in":3600}`))
		case strings.HasPrefix(r.URL.Path, "/jobs/"):
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "not a wake", http.StatusBadRequest)
				return
			}
			f.wakes = append(f.wakes, r.URL.Path)
			f.auth = append(f.auth, r.Header.Get("Authorization"))
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fake) wake(job string) Wake {
	return Wake{URL: f.server.URL + "/jobs/" + job + ":run", Token: &Token{URL: f.server.URL + "/token", Headers: map[string]string{"Metadata-Flavor": "Fake"}}}
}

func (f *fake) list(jobs ...string) []Wake {
	var list []Wake
	for _, job := range jobs {
		list = append(list, f.wake(job))
	}
	return list
}

func (f *fake) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.wakes...)
}

// TestNotifierWakesOncePerQueueInFiveSeconds (D145): an enqueue POSTs the
// queue's wake with the bearer token its token endpoint gives; more
// enqueues of the queue within five seconds send nothing; another queue's
// wake goes out beside it; and once five seconds have passed the queue is
// woken again, with the token the Notifier still holds. A queue with no
// wake sends nothing.
func TestNotifierWakesOncePerQueueInFiveSeconds(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	var clock sync.Mutex
	n := New(map[string][]Wake{"OrderPlaced": f.list("fulfil"), "StockLow": f.list("reorder")}, nil)
	n.Now = func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clock.Lock()
		now = now.Add(d)
		clock.Unlock()
	}
	for i := 0; i < 20; i++ {
		n.Enqueued("OrderPlaced")
	}
	n.Enqueued("Unwatched")
	n.Wait()
	if got := f.sent(); len(got) != 1 || got[0] != "/jobs/fulfil:run" {
		t.Fatalf("twenty enqueues of one queue sent %v, want one wake of its job", got)
	}
	if f.auth[0] != "Bearer t-1" {
		t.Errorf("the wake was sent with %q, want the token the token endpoint gave", f.auth[0])
	}
	n.Enqueued("StockLow")
	n.Wait()
	if got := f.sent(); len(got) != 2 || got[1] != "/jobs/reorder:run" {
		t.Fatalf("another queue's enqueue sent %v, want its own wake beside the first", got)
	}
	advance(Interval - time.Millisecond)
	n.Enqueued("OrderPlaced")
	n.Wait()
	if got := f.sent(); len(got) != 2 {
		t.Fatalf("an enqueue %s after the wake sent another: %v", Interval-time.Millisecond, got)
	}
	advance(time.Millisecond)
	n.Enqueued("OrderPlaced")
	n.Wait()
	if got := f.sent(); len(got) != 3 || got[2] != "/jobs/fulfil:run" {
		t.Fatalf("an enqueue %s after the wake sent %v, want a second wake", Interval, got)
	}
	if f.tokens != 1 {
		t.Errorf("the Notifier asked for %d tokens, want the one it holds", f.tokens)
	}
}

// TestAQueueTwoWorkersDrainWakesBoth: a queue's member lists a wake per
// worker that drains it, and one enqueue sends each once.
func TestAQueueTwoWorkersDrainWakesBoth(t *testing.T) {
	f := newFake(t)
	n := New(map[string][]Wake{"OrderPlaced": f.list("fulfil", "audit")}, nil)
	n.Enqueued("OrderPlaced")
	n.Enqueued("OrderPlaced")
	n.Wait()
	got := f.sent()
	slices.Sort(got)
	if !slices.Equal(got, []string{"/jobs/audit:run", "/jobs/fulfil:run"}) {
		t.Fatalf("two enqueues of a queue two workers drain sent %v, want one wake of each", got)
	}
}

// TestAFailedWakeIsLoggedAndNothingElse: a wake the platform refuses, and
// one whose token cannot be had, are logged as warnings, and Enqueued,
// which the enqueue called, returned before either: the write does not
// wait for the wake and cannot fail with it.
func TestAFailedWakeIsLoggedAndNothingElse(t *testing.T) {
	f := newFake(t)
	f.status = http.StatusForbidden
	core, logs := observer.New(zap.WarnLevel)
	n := New(map[string][]Wake{
		"OrderPlaced": f.list("fulfil"),
		"StockLow":    {{URL: f.server.URL + "/jobs/reorder:run", Token: &Token{URL: f.server.URL + "/token"}}},
		"Unreachable": {{URL: "http://127.0.0.1:1/jobs/none:run"}},
	}, zap.New(core))
	n.Enqueued("OrderPlaced")
	n.Enqueued("StockLow")
	n.Enqueued("Unreachable")
	n.Wait()
	if logs.Len() != 3 {
		t.Fatalf("three failed wakes logged %d warnings: %v", logs.Len(), logs.All())
	}
	causes := map[string]string{}
	for _, entry := range logs.All() {
		fields := entry.ContextMap()
		causes[fields["queue"].(string)] = fields["error"].(string)
		if !strings.Contains(entry.Message, "its tick finds the message") {
			t.Errorf("the warning is %q", entry.Message)
		}
	}
	if !strings.Contains(causes["OrderPlaced"], "the wake answered 403") {
		t.Errorf("a refused wake logged %q", causes["OrderPlaced"])
	}
	if !strings.Contains(causes["StockLow"], "the wake's token: the token request answered 403") {
		t.Errorf("a wake with no token logged %q", causes["StockLow"])
	}
	if causes["Unreachable"] == "" {
		t.Errorf("an unreachable wake logged nothing: %v", causes)
	}
	// A nil Notifier, as FromEnv returns with no wakes, sends nothing.
	var none *Notifier
	none.Enqueued("OrderPlaced")
	none.Wait()
}

// TestParseAndFromEnv: the variable holds a JSON object of wakes by queue;
// unset or empty it is no Notifier at all, and one that is no such object,
// or names a wake with no url, is refused as the deployable starts.
func TestParseAndFromEnv(t *testing.T) {
	t.Setenv(Variable, "")
	if n, err := FromEnv(nil); n != nil || err != nil {
		t.Errorf("FromEnv with no wakes = %v, %v", n, err)
	}
	t.Setenv(Variable, `{"OrderPlaced":[{"url":"https://run.example/jobs/fulfil:run","token":{"url":"http://metadata/token","headers":{"Metadata-Flavor":"Google"}}}]}`)
	n, err := FromEnv(nil)
	if err != nil || n == nil {
		t.Fatalf("FromEnv = %v, %v", n, err)
	}
	if len(n.wakes["OrderPlaced"]) != 1 {
		t.Fatalf("the wakes read as %+v", n.wakes)
	}
	w := n.wakes["OrderPlaced"][0]
	if w.URL != "https://run.example/jobs/fulfil:run" || w.Token == nil || w.Token.URL != "http://metadata/token" || w.Token.Headers["Metadata-Flavor"] != "Google" {
		t.Errorf("the wake reads as %+v", w)
	}
	for value, want := range map[string]string{
		`[]`:                   "is no JSON object of lists of wakes by queue",
		`{"OrderPlaced":[{}]}`: "a wake of queue OrderPlaced has no url",
		`{"OrderPlaced":[{"url":"u","token":{}}]}`: "a wake of queue OrderPlaced names a token with no url",
	} {
		t.Setenv(Variable, value)
		if _, err := FromEnv(nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("FromEnv of %s = %v, want one saying %q", value, err, want)
		}
	}
}
