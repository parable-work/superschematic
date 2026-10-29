package memorystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Signal is the message of a channel that carries no payload: its name is
// the whole message. It encodes as `{}`, and any message decodes to it.
type Signal struct{}

// MarshalJSON encodes a Signal as `{}`.
func (Signal) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// UnmarshalJSON takes any message as a Signal.
func (*Signal) UnmarshalJSON([]byte) error { return nil }

// Channel is one channel of a store, whose messages are T: a definition's
// For builds it from the declaration's parts. A message is a wake-up or a
// sample a replica can lose; a waiter that hears one reads the fact in the
// database.
type Channel[T any] struct {
	ref Ref
}

// NewChannel is the channel of declaration named name, without the
// store's prefix, which Name built. The generated definitions call it.
func NewChannel[T any](declaration, name string) Channel[T] {
	return Channel[T]{ref: Ref{Declaration: declaration, Name: name}}
}

// Ref is the channel's ref: its declaration and its name without the
// store's prefix. A log line or a metric carries the declaration alone.
func (c Channel[T]) Ref() Ref { return c.ref }

// Name is the channel's name without the store's prefix. It holds the
// parts: it is no metric attribute and no log field.
func (c Channel[T]) Name() string { return c.ref.Name }

// Declaration is the channel's declaration's name.
func (c Channel[T]) Declaration() string { return c.ref.Declaration }

// Publish encodes msg as JSON and publishes it on the channel. It returns
// ErrUnavailable at once while s is unavailable, and refuses a message
// over MaxMessageBytes before it sends anything.
func (c Channel[T]) Publish(ctx context.Context, s Store, msg T) error {
	if c.ref.Name == "" {
		return errors.New("memorystore: the channel is not built; build it with its definition's For")
	}
	if s == nil || !s.Available() {
		if s != nil {
			// The store counts the refusal itself, unless it is Disabled.
			return s.Publish(ctx, c.ref, nil)
		}
		return ErrUnavailable
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("memorystore: encode a %s message: %w", c.ref.Declaration, err)
	}
	return s.Publish(ctx, c.ref, data)
}

// Subscribe calls h with each message of the channel that decodes as a T,
// until the subscription is closed. A message that does not decode is
// dropped and counted, never delivered. Unknown keys are ignored at every
// depth, so replicas of two releases share a channel while a deploy
// rolls. It returns ErrUnavailable while s is unavailable: the caller
// polls as it did before the store existed.
func (c Channel[T]) Subscribe(ctx context.Context, s Store, h func(context.Context, T)) (Subscription, error) {
	if c.ref.Name == "" {
		return nil, errors.New("memorystore: the channel is not built; build it with its definition's For")
	}
	if h == nil {
		return nil, fmt.Errorf("memorystore: subscribe to %s with no handler", c.ref.Declaration)
	}
	if s == nil {
		return nil, ErrUnavailable
	}
	declaration := c.ref.Declaration
	return s.Subscribe(ctx, c.ref, func(ctx context.Context, data []byte) {
		var msg T
		if err := json.Unmarshal(data, &msg); err != nil {
			if r, ok := s.(decodeFailureRecorder); ok {
				r.DecodeFailed(ctx, declaration)
			}
			return
		}
		h(ctx, msg)
	})
}
