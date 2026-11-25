// Package wake sends the wake a queue client sends after an enqueue (D145,
// docs/stack-model.md, section 8.8). A worker in drain mode runs as a job
// that empties its queue and exits, so a queue that is empty runs nothing,
// and an enqueue has to start it. A platform that can start one gives each
// deployable that may enqueue to the queue its wakes in Variable, a JSON
// object keyed by the queue's `@queue` class, each member the list of the
// wakes of the workers that drain it: an HTTP POST each, with a bearer
// token when the wake says where one comes from. The package names
// no provider: on gcp the wake is the Cloud Run job's run method and the
// token the metadata server's.
//
// A wake is a hint, never part of the write. A Notifier sends it after the
// enqueue's statement, in a goroutine of its own, at most once per queue in
// Interval per process, and one that fails is logged and nothing else: the
// worker's tick finds the message. An entrypoint builds one with FromEnv
// and hands its Enqueued to the ORM of each database it enqueues through
// (the generated orm package's OnEnqueue).
package wake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Variable is the environment variable that holds the wakes, as a
// platform sets it (ir.QueueWakesVariable).
const Variable = "QUEUE_WAKES"

// Interval is the least time between two wakes of one queue from one
// process.
const Interval = 5 * time.Second

// timeout bounds each of a wake's requests, the token's and the wake's.
const timeout = 10 * time.Second

// Wake is how a queue's worker is started (ir.QueueWake): a POST of an
// empty JSON object to URL, with a bearer token when Token says where one
// comes from.
type Wake struct {
	URL   string `json:"url"`
	Token *Token `json:"token,omitempty"`
}

// Token is where a wake's bearer token comes from: a GET of URL with
// Headers answers a JSON object whose access_token member is the token and
// whose expires_in member is how many seconds it lasts.
type Token struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Parse reads the value of Variable: a JSON object that gives each queue
// the list of its wakes. An empty value is no wakes. It refuses a value
// that is no such object, so a deployable with a broken one fails as it
// starts, not at its first enqueue.
func Parse(value string) (map[string][]Wake, error) {
	if len(bytes.TrimSpace([]byte(value))) == 0 {
		return nil, nil
	}
	var wakes map[string][]Wake
	if err := json.Unmarshal([]byte(value), &wakes); err != nil {
		return nil, fmt.Errorf("%s is no JSON object of lists of wakes by queue: %w", Variable, err)
	}
	for queue, list := range wakes {
		for _, w := range list {
			switch {
			case w.URL == "":
				return nil, fmt.Errorf("%s: a wake of queue %s has no url", Variable, queue)
			case w.Token != nil && w.Token.URL == "":
				return nil, fmt.Errorf("%s: a wake of queue %s names a token with no url", Variable, queue)
			}
		}
	}
	return wakes, nil
}

// Notifier sends the wakes of a set of queues, each queue's at most once
// in Interval. Its zero value sends none, and so does a nil one.
type Notifier struct {
	wakes  map[string][]Wake
	logger *zap.Logger

	// Client makes the requests; nil is http.DefaultClient. Now is the
	// clock; nil is time.Now. A test sets both.
	Client *http.Client
	Now    func() time.Time

	mu     sync.Mutex
	last   map[string]time.Time
	tokens map[string]heldToken
	wg     sync.WaitGroup
}

// heldToken is a token and when the Notifier stops using it.
type heldToken struct {
	value string
	until time.Time
}

// New returns a Notifier of wakes that logs a wake that failed to logger,
// which may be nil.
func New(wakes map[string][]Wake, logger *zap.Logger) *Notifier {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Notifier{wakes: wakes, logger: logger, last: map[string]time.Time{}, tokens: map[string]heldToken{}}
}

// FromEnv returns the Notifier of the wakes the environment gives in
// Variable, or nil when it gives none: a deployable no drain worker's
// queue is reached from sends nothing.
func FromEnv(logger *zap.Logger) (*Notifier, error) {
	wakes, err := Parse(os.Getenv(Variable))
	if err != nil || len(wakes) == 0 {
		return nil, err
	}
	return New(wakes, logger), nil
}

// Enqueued says a message was enqueued on the queue, named by its `@queue`
// class. It returns at once: the queue's wakes, when it has any and none
// was sent within Interval, go out in goroutines of their own, and a
// failure is logged. It is safe on a nil Notifier, which sends nothing.
func (n *Notifier) Enqueued(queue string) {
	if n == nil {
		return
	}
	wakes := n.wakes[queue]
	if len(wakes) == 0 {
		return
	}
	now := n.now()
	n.mu.Lock()
	if sent, ok := n.last[queue]; ok && now.Sub(sent) < Interval {
		n.mu.Unlock()
		return
	}
	n.last[queue] = now
	n.wg.Add(len(wakes))
	n.mu.Unlock()
	for _, w := range wakes {
		go func() {
			defer n.wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*timeout)
			defer cancel()
			if err := n.send(ctx, w); err != nil {
				n.logger.Warn("the wake of a queue's worker failed: its tick finds the message", zap.String("queue", queue), zap.Error(err))
			}
		}()
	}
}

// Wait returns once every wake sent so far has been answered, for a
// process that is about to exit and a test.
func (n *Notifier) Wait() {
	if n != nil {
		n.wg.Wait()
	}
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) client() *http.Client {
	if n.Client != nil {
		return n.Client
	}
	return http.DefaultClient
}

// send POSTs the wake, with its token when it names one.
func (n *Notifier) send(ctx context.Context, w Wake) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.Token != nil {
		token, err := n.token(ctx, w.Token)
		if err != nil {
			return fmt.Errorf("the wake's token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := n.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the wake answered %s", resp.Status)
	}
	return nil
}

// token returns the bearer token t gives, the one it holds while that
// lasts.
func (n *Notifier) token(ctx context.Context, t *Token) (string, error) {
	n.mu.Lock()
	held, ok := n.tokens[t.URL]
	n.mu.Unlock()
	if ok && held.until.After(n.now()) {
		return held.value, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return "", err
	}
	for name, value := range t.Headers {
		req.Header.Set(name, value)
	}
	resp, err := n.client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the token request answered %s", resp.Status)
	}
	var body struct {
		AccessToken string  `json:"access_token"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("the token request answered no access_token")
	}
	// A token is used until a minute before it ends, or for a minute when
	// it does not say.
	lasts := time.Minute
	if body.ExpiresIn > 120 {
		lasts = time.Duration(body.ExpiresIn-60) * time.Second
	}
	n.mu.Lock()
	n.tokens[t.URL] = heldToken{value: body.AccessToken, until: n.now().Add(lasts)}
	n.mu.Unlock()
	return body.AccessToken, nil
}
