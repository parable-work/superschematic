package memorystore

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Key is one key of a store, a counter or a claim, with its TTL: a
// definition's For or ForTTL builds it from the declaration's parts. Every
// key expires. A key holds a count or a claim a replica can lose: when the
// store loses it, the caller counts again from zero or claims again.
type Key struct {
	ref Ref
	ttl time.Duration
}

// NewKey is the key of declaration named name, without the store's prefix,
// which Name built, expiring ttl after the write that creates it. The
// generated definitions call it.
func NewKey(declaration, name string, ttl time.Duration) Key {
	return Key{ref: Ref{Declaration: declaration, Name: name}, ttl: ttl.Truncate(time.Millisecond)}
}

// KeyTTL checks the TTL a caller gives a key of a declaration whose life
// depends on the call: at least a millisecond and at most max, the
// declaration's maxTtlSeconds.
func KeyTTL(declaration string, ttl, max time.Duration) (time.Duration, error) {
	if ttl < time.Millisecond || ttl > max {
		return 0, fmt.Errorf("memorystore: key %s: the TTL %s is not from 1ms to its maximum, %s", declaration, ttl, max)
	}
	return ttl.Truncate(time.Millisecond), nil
}

// Ref is the key's ref: its declaration and its name without the store's
// prefix.
func (k Key) Ref() Ref { return k.ref }

// Name is the key's name without the store's prefix. It holds the parts:
// it is no metric attribute and no log field.
func (k Key) Name() string { return k.ref.Name }

// Declaration is the key's declaration's name.
func (k Key) Declaration() string { return k.ref.Declaration }

// TTL is how long the key lives after the write that creates it.
func (k Key) TTL() time.Duration { return k.ttl }

// Increment adds one to the counter and returns the new count. The
// increment that creates the key sets it to expire after TTL; later ones
// keep that expiry, so the count covers one TTL from its first increment.
// A retry after a lost reply can count one twice: a counter counts high,
// never low.
func (k Key) Increment(ctx context.Context, s Store) (int64, error) {
	if err := k.check(s); err != nil {
		return 0, err
	}
	return s.Increment(ctx, k.ref, k.ttl)
}

// Count returns the counter, 0 when the key does not exist.
func (k Key) Count(ctx context.Context, s Store) (int64, error) {
	if err := k.check(s); err != nil {
		return 0, err
	}
	return s.Count(ctx, k.ref)
}

// Claim sets the claim when no claim of its name exists, expiring after
// TTL, and reports whether this call set it: of the calls that claim one
// key before it expires, one gets true.
func (k Key) Claim(ctx context.Context, s Store) (bool, error) {
	if err := k.check(s); err != nil {
		return false, err
	}
	return s.Claim(ctx, k.ref, k.ttl)
}

// Delete removes the key: a count starts again from zero, and the next
// Claim gets true. It removes another caller's claim too, so a caller
// deletes only to give back its own, when the work the claim guards
// fails.
func (k Key) Delete(ctx context.Context, s Store) error {
	if err := k.check(s); err != nil {
		return err
	}
	return s.Delete(ctx, k.ref)
}

func (k Key) check(s Store) error {
	if k.ref.Name == "" {
		return errors.New("memorystore: the key is not built; build it with its definition's For")
	}
	if s == nil {
		return ErrUnavailable
	}
	return nil
}
