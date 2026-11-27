package providers

import (
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// SecretMask is what a secret prints as: in JSON, in a log line and in
// every fmt verb.
const SecretMask = access.SecretMask

// SecretValue is a secret a provider reads, a setting of a Secret<string>
// config field or a credential on its way to the runner: Reveal alone
// gives its text, and every other form of it, its JSON, its fmt verbs and
// its zap field, prints SecretMask.
type SecretValue struct {
	text *string
}

// NewSecret seals text.
func NewSecret(text string) SecretValue { return SecretValue{text: &text} }

// Reveal is the secret's text, "" for the zero SecretValue.
func (s SecretValue) Reveal() string {
	if s.text == nil {
		return ""
	}
	return *s.text
}

// IsZero reports whether s seals nothing.
func (s SecretValue) IsZero() bool { return s.text == nil }

// String is SecretMask.
func (SecretValue) String() string { return SecretMask }

// GoString is SecretMask, for %#v.
func (SecretValue) GoString() string { return SecretMask }

// Format prints SecretMask for every verb.
func (SecretValue) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(SecretMask)) }

// MarshalJSON is SecretMask, as a JSON string.
func (SecretValue) MarshalJSON() ([]byte, error) { return json.Marshal(SecretMask) }

// MarshalText is SecretMask.
func (SecretValue) MarshalText() ([]byte, error) { return []byte(SecretMask), nil }
