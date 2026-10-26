package memorystore

import (
	"context"
	"slices"
	"sync"

	"go.uber.org/zap"
)

// Fanout holds a store's local handlers, by channel. The process holds
// one subscription to the server per channel, and Deliver wakes every
// local handler of it, in the order they subscribed, on the caller's
// goroutine. A handler that panics is logged and skipped; the others still
// run.
type Fanout struct {
	mu       sync.Mutex
	channels map[string]*fanoutChannel
	next     uint64
	logger   *zap.Logger
}

type fanoutChannel struct {
	declaration string
	handlers    []fanoutHandler
}

type fanoutHandler struct {
	id uint64
	h  func(context.Context, []byte)
}

// NewFanout is an empty Fanout that logs a handler's panic to logger, nil
// for none.
func NewFanout(logger *zap.Logger) *Fanout {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Fanout{channels: map[string]*fanoutChannel{}, logger: logger}
}

// Add adds h as a handler of the channel full names, of declaration, and
// reports whether it is the channel's first, for which the store
// subscribes to the server. Remove takes the id back.
func (f *Fanout) Add(full, declaration string, h func(context.Context, []byte)) (id uint64, first bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	ch := f.channels[full]
	if ch == nil {
		ch = &fanoutChannel{declaration: declaration}
		f.channels[full] = ch
		first = true
	}
	ch.handlers = append(ch.handlers, fanoutHandler{id: f.next, h: h})
	return f.next, first
}

// Remove removes the handler id of the channel full names, and reports
// whether it was the channel's last, for which the store unsubscribes.
func (f *Fanout) Remove(full string, id uint64) (last bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := f.channels[full]
	if ch == nil {
		return false
	}
	i := slices.IndexFunc(ch.handlers, func(h fanoutHandler) bool { return h.id == id })
	if i < 0 {
		return false
	}
	ch.handlers = slices.Delete(ch.handlers, i, i+1)
	if len(ch.handlers) > 0 {
		return false
	}
	delete(f.channels, full)
	return true
}

// Channels are the full names of the channels with a handler.
func (f *Fanout) Channels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.channels))
	for name := range f.channels {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Deliver calls every handler of the channel full names with data, and
// returns the channel's declaration and whether it had a handler.
func (f *Fanout) Deliver(ctx context.Context, full string, data []byte) (declaration string, ok bool) {
	f.mu.Lock()
	ch := f.channels[full]
	var handlers []fanoutHandler
	if ch != nil {
		declaration = ch.declaration
		handlers = slices.Clone(ch.handlers)
	}
	f.mu.Unlock()
	if ch == nil {
		return "", false
	}
	for _, h := range handlers {
		f.call(ctx, declaration, h.h, data)
	}
	return declaration, true
}

func (f *Fanout) call(ctx context.Context, declaration string, h func(context.Context, []byte), data []byte) {
	defer func() {
		if r := recover(); r != nil {
			f.logger.Error("memory store handler panicked; the message is dropped for it", zap.String("declaration", declaration), zap.Any("panic", r))
		}
	}()
	h(ctx, data)
}

// subscription is a Subscription whose Close runs once.
type subscription struct {
	once  sync.Once
	close func() error
	err   error
}

// NewSubscription is a Subscription whose Close runs close once.
func NewSubscription(close func() error) Subscription {
	return &subscription{close: close}
}

func (s *subscription) Close() error {
	s.once.Do(func() { s.err = s.close() })
	return s.err
}
