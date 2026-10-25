package memorystore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// InProcess is a store in one process, with the semantics of the Redis
// one: a key expires at its TTL against the store's clock, which a test
// sets, a counter's TTL is set by the increment that creates it, a claim
// is set only when absent, and a message reaches every handler subscribed
// to its channel in the process, on a delivery goroutine of the store's,
// in the order published. SetAvailable takes it down and up, as a server
// that goes away would be.
type InProcess struct {
	name    string
	metrics *Metrics
	fanout  *Fanout

	mu    sync.Mutex
	now   func() time.Time
	keys  map[string]inProcessKey
	queue chan inProcessMessage

	available atomic.Bool
	closed    atomic.Bool
	closeOnce sync.Once
	done      chan struct{}
}

type inProcessKey struct {
	value   int64
	expires time.Time
}

type inProcessMessage struct {
	full string
	data []byte
}

// InProcessOption configures NewInProcess.
type InProcessOption func(*InProcess)

// WithClock sets the clock a key's expiry is measured against.
func WithClock(now func() time.Time) InProcessOption {
	return func(s *InProcess) { s.now = now }
}

// WithMeterProvider records the store's metrics on provider's meter in
// place of otel's global one.
func WithMeterProvider(provider metric.MeterProvider) InProcessOption {
	return func(s *InProcess) {
		s.metrics.Close()
		s.metrics = NewMetrics(provider, s.name, s.Available)
	}
}

// NewInProcess is an available in-process store whose prefix is name.
func NewInProcess(name string, opts ...InProcessOption) *InProcess {
	s := &InProcess{
		name:   name,
		fanout: NewFanout(nil),
		now:    time.Now,
		keys:   map[string]inProcessKey{},
		queue:  make(chan inProcessMessage, 1024),
		done:   make(chan struct{}),
	}
	s.available.Store(true)
	s.metrics = NewMetrics(nil, name, s.Available)
	for _, opt := range opts {
		opt(s)
	}
	go s.deliver()
	return s
}

var _ Store = (*InProcess)(nil)

// Name is the store's prefix.
func (s *InProcess) Name() string { return s.name }

// Available reports whether the store takes calls: true until SetAvailable
// takes it down or Close closes it.
func (s *InProcess) Available() bool { return !s.closed.Load() && s.available.Load() }

// SetAvailable takes the store down or brings it back. A store that is
// down refuses every call but Subscribe with ErrUnavailable and delivers
// no message; its keys and its subscriptions stay, as a server's keys and
// a Redis store's subscriptions do through a network failure.
func (s *InProcess) SetAvailable(up bool) { s.available.Store(up) }

// SetClock sets the clock a key's expiry is measured against.
func (s *InProcess) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// DecodeFailed counts a message of declaration a typed channel dropped.
func (s *InProcess) DecodeFailed(ctx context.Context, declaration string) {
	s.metrics.DecodeFailure(ctx, declaration)
}

// Publish queues data for every handler of the channel ref names.
func (s *InProcess) Publish(ctx context.Context, ref Ref, data []byte) error {
	if err := CheckRef(ref); err != nil {
		return err
	}
	if !s.Available() {
		s.metrics.Publish(ctx, ref.Declaration, OutcomeUnavailable)
		return ErrUnavailable
	}
	if len(data) > MaxMessageBytes {
		s.metrics.Publish(ctx, ref.Declaration, OutcomeTooLarge)
		return fmt.Errorf("memorystore: a %s message is %d bytes, over the %d a message holds", ref.Declaration, len(data), MaxMessageBytes)
	}
	msg := inProcessMessage{full: FullName(s.name, ref), data: append([]byte(nil), data...)}
	select {
	case s.queue <- msg:
	case <-ctx.Done():
		s.metrics.Publish(ctx, ref.Declaration, OutcomeCanceled)
		return ctx.Err()
	case <-s.done:
		s.metrics.Publish(ctx, ref.Declaration, OutcomeUnavailable)
		return ErrUnavailable
	}
	s.metrics.Publish(ctx, ref.Declaration, OutcomeOK)
	return nil
}

// Subscribe adds h as a handler of the channel ref names, also while the
// store is down.
func (s *InProcess) Subscribe(_ context.Context, ref Ref, h func(context.Context, []byte)) (Subscription, error) {
	if err := CheckRef(ref); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, fmt.Errorf("memorystore: subscribe to %s with no handler", ref.Declaration)
	}
	// A store that is down holds the subscription, as one on Redis does
	// until it reconnects; only a closed one refuses it.
	if s.closed.Load() {
		return nil, ErrUnavailable
	}
	full := FullName(s.name, ref)
	id, _ := s.fanout.Add(full, ref.Declaration, h)
	return NewSubscription(func() error {
		s.fanout.Remove(full, id)
		return nil
	}), nil
}

// Increment adds one to the counter ref names.
func (s *InProcess) Increment(ctx context.Context, ref Ref, ttl time.Duration) (int64, error) {
	if err := s.ready(ctx, ref, OpIncrement); err != nil {
		return 0, err
	}
	if err := CheckTTL(ttl); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	full := FullName(s.name, ref)
	k, ok := s.live(full)
	if !ok {
		k = inProcessKey{expires: s.now().Add(ttl.Truncate(time.Millisecond))}
	}
	k.value++
	s.keys[full] = k
	s.metrics.Key(ctx, ref.Declaration, OpIncrement, OutcomeOK)
	return k.value, nil
}

// Count returns the counter ref names, 0 when absent.
func (s *InProcess) Count(ctx context.Context, ref Ref) (int64, error) {
	if err := s.ready(ctx, ref, OpCount); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, _ := s.live(FullName(s.name, ref))
	s.metrics.Key(ctx, ref.Declaration, OpCount, OutcomeOK)
	return k.value, nil
}

// Claim sets the claim ref names when it is absent.
func (s *InProcess) Claim(ctx context.Context, ref Ref, ttl time.Duration) (bool, error) {
	if err := s.ready(ctx, ref, OpClaim); err != nil {
		return false, err
	}
	if err := CheckTTL(ttl); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	full := FullName(s.name, ref)
	s.metrics.Key(ctx, ref.Declaration, OpClaim, OutcomeOK)
	if _, ok := s.live(full); ok {
		return false, nil
	}
	s.keys[full] = inProcessKey{value: 1, expires: s.now().Add(ttl.Truncate(time.Millisecond))}
	return true, nil
}

// Delete removes the key ref names.
func (s *InProcess) Delete(ctx context.Context, ref Ref) error {
	if err := s.ready(ctx, ref, OpDelete); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, FullName(s.name, ref))
	s.metrics.Key(ctx, ref.Declaration, OpDelete, OutcomeOK)
	return nil
}

// TTL is how long the key ref names has left, and false when it is
// absent: what a test reads where a server answers PTTL.
func (s *InProcess) TTL(ref Ref) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.live(FullName(s.name, ref))
	if !ok {
		return 0, false
	}
	return k.expires.Sub(s.now()), true
}

// Close closes the store: every call after it returns ErrUnavailable, and
// the delivery goroutine ends.
func (s *InProcess) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done)
		s.metrics.Close()
	})
	return nil
}

// ready refuses a ref Name did not build, and a call while the store is
// unavailable.
func (s *InProcess) ready(ctx context.Context, ref Ref, op string) error {
	if err := CheckRef(ref); err != nil {
		return err
	}
	if !s.Available() {
		s.metrics.Key(ctx, ref.Declaration, op, OutcomeUnavailable)
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		s.metrics.Key(ctx, ref.Declaration, op, OutcomeCanceled)
		return err
	}
	return nil
}

// live is the key full names unless it has expired, which it drops. The
// caller holds mu.
func (s *InProcess) live(full string) (inProcessKey, bool) {
	k, ok := s.keys[full]
	if !ok {
		return inProcessKey{}, false
	}
	if !s.now().Before(k.expires) {
		delete(s.keys, full)
		return inProcessKey{}, false
	}
	return k, true
}

func (s *InProcess) deliver() {
	for {
		select {
		case <-s.done:
			return
		case msg := <-s.queue:
			if !s.Available() {
				continue
			}
			ctx := context.Background()
			if declaration, ok := s.fanout.Deliver(ctx, msg.full, msg.data); ok {
				s.metrics.Receive(ctx, declaration)
			}
		}
	}
}
