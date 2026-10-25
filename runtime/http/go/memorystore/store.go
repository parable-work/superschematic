// Package memorystore is the provider-neutral runtime of a memory store an
// API lists in its config's memoryStores (D75): a Redis or Valkey server
// that the replicas of a stack's servers, jobs and workers share, and whose
// loss costs a sample, never a record.
//
// Store is the low-level store the generated Deps holds, one per listed
// store. A caller never names a channel or a key itself: the store's typed
// definitions, which its MemoryStore service's build writes (`shopcache`),
// build each Channel and Key from the declaration's parts, through Name.
//
// The code works without the store. A Disabled store, and a store that is
// closed or not connected, refuses every call at once with ErrUnavailable,
// and the caller takes the path it had before the store existed. A process
// opens each store once, with the implementation its provider needs: the
// generated entrypoint's memorystores.go opens one on Redis, which only an
// entrypoint whose APIs list a store links, so this package requires no
// Redis client. NewInProcess is a store with real semantics in one process,
// for tests.
//
// The package also holds what a networked implementation shares and needs
// no client for: Watch (availability, the health ping and its backoff),
// Fanout (one subscription per channel, which wakes every local handler),
// the metrics, MetadataTokens (an access token per connection for `auth:
// iam`) and Redact, and the shared rate limit's httprate counter
// (RateLimitCounter).
package memorystore

import (
	"context"
	"errors"
	"time"

	"github.com/parable-work/superschematic/ir"
)

// ErrUnavailable is the error a store returns, or wraps, when it cannot
// take a call: it is disabled, closed or not connected, or the connection
// failed during the call. A store that is unavailable returns it at once,
// without waiting for the server. A caller that gets it takes its
// fallback path.
var ErrUnavailable = errors.New("memorystore: the memory store is unavailable")

// MaxMessageBytes is the most bytes one encoded message of a channel
// holds: 64 KiB. Publish refuses a larger one before it sends anything.
const MaxMessageBytes = ir.MemoryStoreMaxMessageBytes

// Ref names one channel or key of a store. Name is the name without the
// store's prefix (`stock-changed:3f2a...`), which Name builds; the store
// prepends `<prefix>:`. Declaration is the declaration's name
// (`stock-changed`), the one thing of a ref a metric or a log line
// carries.
type Ref struct {
	Declaration string
	Name        string
}

// Subscription is one handler's subscription to a channel. Close stops
// delivering the channel's messages to the handler; it is safe to call
// more than once.
type Subscription interface {
	Close() error
}

// Store is one memory store, opened once per process and shared by every
// API that lists it. It is safe for concurrent use.
//
// Every call but Name, Available and Close returns ErrUnavailable, or an
// error that wraps it, while the store is unavailable. A reply the server
// refuses, such as an out-of-memory error, does not wrap it.
type Store interface {
	// Name is the store's prefix: the MemoryStore service's name, or
	// `<store>@<member>` in a member of a parameterized environment.
	Name() string

	// Available reports whether the store takes calls now: false for a
	// disabled or closed store, and while it is not connected.
	Available() bool

	// Publish sends data to the channel ref names. A message is a wake-up
	// or a sample a replica can lose: nobody may be subscribed, and a
	// subscriber that is not connected misses it. A message over
	// MaxMessageBytes is refused.
	Publish(ctx context.Context, ref Ref, data []byte) error

	// Subscribe delivers each message of the channel ref names to h, until
	// the subscription is closed. The process holds one subscription per
	// channel, which wakes every local handler. h runs on the store's
	// delivery goroutine: it returns quickly and hands slow work on.
	Subscribe(ctx context.Context, ref Ref, h func(context.Context, []byte)) (Subscription, error)

	// Increment adds one to the counter ref names and returns the new
	// count. The increment that creates the key sets it to expire after
	// ttl; later ones keep that expiry.
	Increment(ctx context.Context, ref Ref, ttl time.Duration) (int64, error)

	// Count returns the counter ref names: 0 when there is no such key.
	Count(ctx context.Context, ref Ref) (int64, error)

	// Claim sets the claim ref names, expiring after ttl, when no claim of
	// that name exists, and reports whether this call set it.
	Claim(ctx context.Context, ref Ref, ttl time.Duration) (bool, error)

	// Delete removes the key ref names: a count starts again from zero and
	// the next Claim gets true. Deleting a key that does not exist is no
	// error.
	Delete(ctx context.Context, ref Ref) error

	// Close closes the store's connections. Every call after it returns
	// ErrUnavailable. It is safe to call more than once.
	Close() error
}

// Disabled is a store that takes no call: Available is false and every
// call returns ErrUnavailable. A workload whose field is unset, or whose
// field names a store it cannot open, runs with one. It records no
// metric.
func Disabled(name string) Store {
	return disabled{name: name}
}

type disabled struct{ name string }

func (d disabled) Name() string                    { return d.name }
func (disabled) Available() bool                   { return false }
func (disabled) Close() error                      { return nil }
func (disabled) Delete(context.Context, Ref) error { return ErrUnavailable }

func (disabled) Publish(context.Context, Ref, []byte) error { return ErrUnavailable }

func (disabled) Subscribe(context.Context, Ref, func(context.Context, []byte)) (Subscription, error) {
	return nil, ErrUnavailable
}

func (disabled) Increment(context.Context, Ref, time.Duration) (int64, error) {
	return 0, ErrUnavailable
}

func (disabled) Count(context.Context, Ref) (int64, error) { return 0, ErrUnavailable }

func (disabled) Claim(context.Context, Ref, time.Duration) (bool, error) {
	return false, ErrUnavailable
}

// IsDisabled reports whether s is a Disabled store, which records no
// metric.
func IsDisabled(s Store) bool {
	_, ok := s.(disabled)
	return ok
}

// FullName is the name the store keys or publishes ref under: its prefix,
// `:`, and ref's name.
func FullName(prefix string, ref Ref) string {
	return prefix + ":" + ref.Name
}

// CheckRef refuses a ref that Name did not build: an empty name, or a
// declaration that is no segment.
func CheckRef(ref Ref) error {
	if ref.Name == "" {
		return errors.New("memorystore: the ref has no name; build it with the store's definitions")
	}
	if err := ir.CheckMemoryStoreSegment(ref.Declaration); err != nil {
		return errors.New("memorystore: the ref's declaration is no segment")
	}
	return nil
}

// CheckTTL refuses a key's TTL below one millisecond, which the server
// cannot keep, or above the longest a key may have, seven days.
func CheckTTL(ttl time.Duration) error {
	if ttl < time.Millisecond {
		return errors.New("memorystore: a key's TTL is at least one millisecond")
	}
	if ttl > MaxTTL {
		return errors.New("memorystore: a key's TTL is at most seven days")
	}
	return nil
}

// MaxTTL is the longest TTL a key may have: seven days.
const MaxTTL = ir.MemoryStoreMaxTTLSeconds * time.Second
