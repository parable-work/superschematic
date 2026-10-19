package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestABackgroundEngineOpensAndSetsUp: a Background opens its engine on
// its goroutine, runs its setup on it, a deploy, and hands it out once the
// setup passed; it is ready then, and closing it closes the engine.
func TestABackgroundEngineOpensAndSetsUp(t *testing.T) {
	var opened atomic.Pointer[Engine]
	b := OpenInBackground(Config{Path: filepath.Join(t.TempDir(), "engine.db"), Policy: AllowAll()}, BackgroundOptions{
		Setup: func(ctx context.Context, e *Engine) error {
			_, err := e.Deploy(ctx, []json.RawMessage{noteSchema}, DeployOptions{})
			return err
		},
		OnOpen: func(e *Engine) { opened.Store(e) },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e, err := b.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Ready(ctx); err != nil {
		t.Errorf("Ready = %v", err)
	}
	if live, err := e.Call(ctx, "schemas.live", alice, "Note"); err != nil || !strings.Contains(string(live), `"version":1`) {
		t.Errorf("the setup's deploy left %s, %v", live, err)
	}
	for deadline := time.Now().Add(time.Second); opened.Load() == nil && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if opened.Load() != e {
		t.Errorf("OnOpen was told of %p, want %p", opened.Load(), e)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Engine(); !errors.Is(err, ErrClosed) {
		t.Errorf("Engine after Close = %v, want ErrClosed", err)
	}
	if _, err := e.Call(ctx, "schemas.live", alice, "Note"); !errors.Is(err, ErrClosed) {
		t.Errorf("a call after Close = %v, want ErrClosed", err)
	}
}

// TestABackgroundEngineRetriesUntilItsDatabaseAnswers: an engine whose
// database does not answer is not open, and says why, its readiness
// fails, each failure is reported with the wait before the next attempt,
// which doubles, and a failing setup counts as a failure; Close stops the
// attempts.
func TestABackgroundEngineRetriesUntilItsDatabaseAnswers(t *testing.T) {
	failures := make(chan time.Duration, 16)
	b := OpenInBackground(Config{
		Postgres: &Postgres{URL: "postgres://nobody@127.0.0.1:9/nothing?connect_timeout=1&sslmode=disable"},
		Policy:   AllowAll(),
	}, BackgroundOptions{
		RetryMin: 10 * time.Millisecond,
		RetryMax: 40 * time.Millisecond,
		OnError:  func(_ error, retry time.Duration) { failures <- retry },
	})
	defer b.Close()
	var waits []time.Duration
	for len(waits) < 4 {
		select {
		case retry := <-failures:
			waits = append(waits, retry)
		case <-time.After(20 * time.Second):
			t.Fatalf("no failure reported after %v", waits)
		}
	}
	if want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond}; !equalDurations(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
	if _, err := b.Engine(); !errors.Is(err, ErrNotOpen) || !strings.Contains(err.Error(), "open: ") {
		t.Errorf("Engine = %v, want ErrNotOpen with the open's failure", err)
	}
	if err := b.Ready(context.Background()); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Ready = %v, want ErrNotOpen", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrNotOpen) {
		t.Errorf("Wait = %v, want the deadline and ErrNotOpen", err)
	}

	setups := 0
	s := OpenInBackground(Config{Path: filepath.Join(t.TempDir(), "engine.db"), Policy: AllowAll()}, BackgroundOptions{
		RetryMin: time.Millisecond,
		Setup: func(context.Context, *Engine) error {
			setups++
			if setups < 3 {
				return errors.New("deploy: not yet")
			}
			return nil
		},
	})
	defer s.Close()
	wait, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	if _, err := s.Wait(wait); err != nil || setups != 3 {
		t.Errorf("a setup that passes the third time: %v after %d setups", err, setups)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
