package codegen

import "time"

// Clock is a function that returns the current time.
// In production this wraps time.Now. In tests it can be replaced
// with a fixed-time function for deterministic output.
type Clock func() time.Time

// DefaultClock returns a Clock that uses time.Now.
func DefaultClock() Clock {
	return time.Now
}

// FixedClock returns a Clock pinned to t, for deterministic test output.
func FixedClock(t time.Time) Clock {
	return func() time.Time { return t }
}

// RFC3339 returns the clock's current time formatted as RFC3339 in UTC.
// This is the standard timestamp format used in generated file headers.
func (c Clock) RFC3339() string {
	return c().UTC().Format(time.RFC3339)
}
