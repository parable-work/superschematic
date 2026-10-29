package ir

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// The memory store (D75). A memory store is a service of the core kind
// MemoryStore: a Redis or Valkey server that the replicas of a stack's
// servers, jobs and workers share, and whose loss costs a sample, never a
// record. An API lists the stores it uses in its config's memoryStores,
// and may name one of them its rateLimitStore. The store's own schema
// declares each channel and key it carries with `@channel` or `@key` on an
// abstract class (MemoryStoreDeclaration): the class is a declaration, no
// type, and its fields are the declaration's parts. Each MemoryStore
// service in a stack is a deployable of kind memoryStore, and the server,
// jobs and workers of each API that lists it reach it over a memoryStore
// edge, whose field holds a MemoryStoreConnection.

// MemoryStoreKind is what a declaration declares: a channel, or a key a
// counter or a claim.
type MemoryStoreKind string

const (
	// MemoryStoreChannel is a pub/sub channel. A message is a wake-up or a
	// sample a replica can lose; a channel with no payload is a signal,
	// whose name is the whole message.
	MemoryStoreChannel MemoryStoreKind = "channel"

	// MemoryStoreCounter is a key that counts: Increment sets its TTL on
	// the write that creates it.
	MemoryStoreCounter MemoryStoreKind = "counter"

	// MemoryStoreClaim is a key that lets one caller do a piece of work
	// once: Claim is a set-if-absent with the TTL, and Delete gives it
	// back.
	MemoryStoreClaim MemoryStoreKind = "claim"
)

// IsKey reports whether k is a key's kind: a counter or a claim.
func (k MemoryStoreKind) IsKey() bool {
	return k == MemoryStoreCounter || k == MemoryStoreClaim
}

// MemoryStorePartFormat is how a part's value is written into a name: the
// wire form of its scalar as a string.
type MemoryStorePartFormat string

const (
	// MemoryStorePartString is a string, or a scalar whose wire form is a
	// string (a UUID's canonical lowercase form, say), written as it is.
	MemoryStorePartString MemoryStorePartFormat = "string"

	// MemoryStorePartInteger is an integer, or a scalar whose wire form is
	// one, written in base 10.
	MemoryStorePartInteger MemoryStorePartFormat = "integer"

	// MemoryStorePartBoolean is a boolean, written `true` or `false`.
	MemoryStorePartBoolean MemoryStorePartFormat = "boolean"

	// MemoryStorePartEnum is an enum, written as its member's wire value.
	MemoryStorePartEnum MemoryStorePartFormat = "enum"
)

// MemoryStoreDeclaration is one `@channel` or `@key` class of a
// MemoryStore schema: one channel or key the store carries. The loaders
// record it in Schema.MemoryStoreDeclarations and in no other place.
type MemoryStoreDeclaration struct {
	// Name is the declaration's name, one segment: the class's name in
	// kebab case (`stock-changed`), or the decorator's `name`.
	Name string `json:"name" yaml:"name"`

	// Symbol is the class's name (`StockChanged`), after which each
	// language's generated definition is named.
	Symbol string `json:"symbol" yaml:"symbol"`

	// Kind is channel, counter or claim.
	Kind MemoryStoreKind `json:"kind" yaml:"kind"`

	// Parts are the class's fields in declaration order, each a required
	// scalar: what a caller passes to the definition's For, and the
	// segments that follow the declaration's name.
	Parts []MemoryStorePart `json:"parts,omitempty" yaml:"parts,omitempty"`

	// Payload is the type of a channel's messages: an object type of a
	// dependency of the store's schema, named as the schema's imports name
	// it. Nil is a signal, whose name is the whole message, and nil on
	// every key.
	Payload *TypeRef `json:"payload,omitempty" yaml:"payload,omitempty"`

	// TTLSeconds is a key's TTL from the write that creates it.
	TTLSeconds int `json:"ttlSeconds,omitempty" yaml:"ttlSeconds,omitempty"`

	// MaxTTLSeconds is the longest TTL a call may give a key whose life
	// depends on the call, such as a rate limit's window. A key sets
	// exactly one of TTLSeconds and MaxTTLSeconds; a channel neither.
	MaxTTLSeconds int `json:"maxTtlSeconds,omitempty" yaml:"maxTtlSeconds,omitempty"`

	// Doc is the class's doc comment.
	Doc string `json:"doc,omitempty" yaml:"doc,omitempty"`

	// File is the schema file that declares the class, relative to the
	// service's directory.
	File string `json:"file,omitempty" yaml:"file,omitempty"`

	// Reserved marks a declaration the core makes in every MemoryStore
	// schema: rate-limit (MemoryStoreRateLimit). A schema declares none
	// of them, and the generators write no definition for one.
	Reserved bool `json:"reserved,omitempty" yaml:"reserved,omitempty"`
}

// MemoryStorePart is one part of a declaration: a field of its class.
type MemoryStorePart struct {
	// Name is the field's name (`productId`).
	Name string `json:"name" yaml:"name"`

	// Type is the field's type: a primitive, a scalar or an enum, never a
	// list or a map.
	Type TypeRef `json:"type" yaml:"type"`

	// Format is how the part's value is written into a name, decided from
	// its type when the schema loads. A data form may leave it out.
	Format MemoryStorePartFormat `json:"format,omitempty" yaml:"format,omitempty"`
}

// The bounds of a declaration.
const (
	// MemoryStoreSegmentMaxBytes is the most bytes a segment holds: a
	// declaration's name, a store's name and each part's value.
	MemoryStoreSegmentMaxBytes = 128

	// MemoryStoreMaxTTLSeconds is the longest TTL a key may have: seven
	// days.
	MemoryStoreMaxTTLSeconds = 7 * 24 * 60 * 60

	// MemoryStoreMaxMessageBytes is the most bytes one encoded message of
	// a channel holds: 64 KiB.
	MemoryStoreMaxMessageBytes = 64 * 1024
)

// MemoryStoreRateLimit is the name of the reserved declaration the core
// makes in every MemoryStore schema: the counters of the shared rate limit
// of each API whose rateLimitStore the store is.
const MemoryStoreRateLimit = "rate-limit"

// The parts of the rate-limit declaration, in order.
const (
	// MemoryStoreRateLimitWindowSlackSeconds is what a rate-limit
	// counter's TTL adds to two windows: a counter lives 2W + 5s.
	MemoryStoreRateLimitWindowSlackSeconds = 5

	// MemoryStoreRateLimitMaxTTLSeconds is the rate-limit declaration's
	// maxTtlSeconds: a day and the slack, so a window of up to twelve
	// hours fits.
	MemoryStoreRateLimitMaxTTLSeconds = 24*60*60 + MemoryStoreRateLimitWindowSlackSeconds
)

// MemoryStoreRateLimitDeclaration returns the reserved rate-limit
// declaration: a counter whose parts are the API service, the operation,
// the window's start in Unix seconds and the client, the lowercase hex
// SHA-256 of the limiter's client key, so an address is no part of a name.
func MemoryStoreRateLimitDeclaration() *MemoryStoreDeclaration {
	return &MemoryStoreDeclaration{
		Name:   MemoryStoreRateLimit,
		Symbol: "RateLimit",
		Kind:   MemoryStoreCounter,
		Parts: []MemoryStorePart{
			{Name: "service", Type: TypeRef{Name: "string"}, Format: MemoryStorePartString},
			{Name: "operation", Type: TypeRef{Name: "string"}, Format: MemoryStorePartString},
			{Name: "window", Type: TypeRef{Name: "Generic.Int64"}, Format: MemoryStorePartInteger},
			{Name: "client", Type: TypeRef{Name: "string"}, Format: MemoryStorePartString},
		},
		MaxTTLSeconds: MemoryStoreRateLimitMaxTTLSeconds,
		Doc:           "The counters of the shared rate limit of each API whose rateLimitStore this store is: one per service, operation, window and client.",
		Reserved:      true,
	}
}

// MemoryStoreDeclaration returns the declaration named name, or nil.
func (s *Schema) MemoryStoreDeclaration(name string) *MemoryStoreDeclaration {
	for _, decl := range s.MemoryStoreDeclarations {
		if decl != nil && decl.Name == name {
			return decl
		}
	}
	return nil
}

// AddReservedMemoryStoreDeclarations adds the core's reserved declarations
// to a MemoryStore schema that lacks them, keeping the declarations sorted
// by name. A schema of another kind is left as it is.
func (s *Schema) AddReservedMemoryStoreDeclarations() {
	if s.Kind != SchemaKindMemoryStore || s.MemoryStoreDeclaration(MemoryStoreRateLimit) != nil {
		return
	}
	s.MemoryStoreDeclarations = append(s.MemoryStoreDeclarations, MemoryStoreRateLimitDeclaration())
	SortMemoryStoreDeclarations(s.MemoryStoreDeclarations)
}

// SortMemoryStoreDeclarations sorts declarations by name.
func SortMemoryStoreDeclarations(decls []*MemoryStoreDeclaration) {
	slices.SortStableFunc(decls, func(a, b *MemoryStoreDeclaration) int {
		return strings.Compare(memoryStoreDeclName(a), memoryStoreDeclName(b))
	})
}

func memoryStoreDeclName(d *MemoryStoreDeclaration) string {
	if d == nil {
		return ""
	}
	return d.Name
}

// The shapes of a declaration's name: derived from a class's name, kebab
// case; given in the decorator, which may also hold `.` and `_`.
var (
	memoryStoreDerivedName  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	memoryStoreExplicitName = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)
)

// MemoryStoreDeclarationName is a class's name in kebab case:
// `StockChanged` is `stock-changed`, `HTTPRequests` `http-requests`.
func MemoryStoreDeclarationName(symbol string) string {
	runes := []rune(symbol)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('-')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		if r == '_' {
			b.WriteByte('-')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// CheckMemoryStoreDeclarationName checks a declaration's name: a segment
// that starts with a lowercase letter, then lowercase letters, digits and
// hyphens, or, for a name the decorator gives, `.` and `_` too.
func CheckMemoryStoreDeclarationName(name string, explicit bool) error {
	if err := CheckMemoryStoreSegment(name); err != nil {
		return err
	}
	if explicit {
		if !memoryStoreExplicitName.MatchString(name) {
			return fmt.Errorf("name %q is not a declaration's name: it starts with a lowercase letter, then lowercase letters, digits, '.', '_' and '-'", name)
		}
		return nil
	}
	if !memoryStoreDerivedName.MatchString(name) {
		return fmt.Errorf("name %q is not a declaration's name: it starts with a lowercase letter, then lowercase letters, digits and '-'; give the declaration a name in the decorator", name)
	}
	return nil
}

// CheckMemoryStoreSegment checks one segment of a full name: a store's
// name, a declaration's name or a part's value. A segment is 1 to 128
// bytes, with no `:`, no glob character (`*`, `?`, `[`, `]`), no `@`, no
// `\` and no control character (a byte below 0x20, or 0x7f). A value that
// breaks the rule is refused, never escaped.
func CheckMemoryStoreSegment(segment string) error {
	switch {
	case segment == "":
		return fmt.Errorf("a segment is empty; it holds 1 to %d bytes", MemoryStoreSegmentMaxBytes)
	case len(segment) > MemoryStoreSegmentMaxBytes:
		return fmt.Errorf("a segment is %d bytes; it holds at most %d", len(segment), MemoryStoreSegmentMaxBytes)
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case c < 0x20 || c == 0x7f:
			return fmt.Errorf("a segment holds the control byte 0x%02x at %d", c, i)
		case strings.IndexByte(memoryStoreRefusedBytes, c) >= 0:
			return fmt.Errorf("a segment holds %q at %d; a segment has no ':', '*', '?', '[', ']', '@' or '\\'", c, i)
		}
	}
	return nil
}

// memoryStoreRefusedBytes are the printable bytes no segment holds: the
// separator, the glob characters, the member's marker and the escape.
const memoryStoreRefusedBytes = `:*?[]@\`

// MemoryStoreName joins a declaration's name and its parts' values into
// a name without the store's prefix (`stock-changed:3f2a...`), checking
// each as a segment. The store prepends `<store>:` or `<store>@<member>:`.
func MemoryStoreName(declaration string, parts ...string) (string, error) {
	if err := CheckMemoryStoreSegment(declaration); err != nil {
		return "", fmt.Errorf("declaration %q: %w", declaration, err)
	}
	var b strings.Builder
	b.WriteString(declaration)
	for i, part := range parts {
		if err := CheckMemoryStoreSegment(part); err != nil {
			return "", fmt.Errorf("declaration %s part %d: %w", declaration, i, err)
		}
		b.WriteByte(':')
		b.WriteString(part)
	}
	return b.String(), nil
}

// MemoryStorePrefix is a store's prefix: its name, then `@` and the
// member's name in a member of a parameterized environment.
func MemoryStorePrefix(store, member string) string {
	if member == "" {
		return store
	}
	return store + "@" + member
}

// NamePattern is the declaration's full name with each part a placeholder,
// as a reviewer reads it: `{store}:stock-changed:{productId}`.
func (d *MemoryStoreDeclaration) NamePattern() string {
	var b strings.Builder
	b.WriteString("{store}:")
	b.WriteString(d.Name)
	for _, part := range d.Parts {
		b.WriteString(":{")
		b.WriteString(part.Name)
		b.WriteByte('}')
	}
	return b.String()
}

// MemoryStoreConnection is what a memoryStore edge's connector derives:
// how a server, a job or a worker reaches a memory store an API of it
// lists (D75). It holds no credential: on gcp the workload authenticates
// as its own account with an access token, and locally the server checks
// none. A platform sets one variable per member, as every derived field's
// (DerivedVariables): `SHOP_CACHE_MEMORY_STORE_URL`, and so on. A field
// none of whose variables is set is a disabled store.
type MemoryStoreConnection struct {
	// Store is the MemoryStore service's name (`shop-cache`), the first
	// segment of every name the store holds for it.
	Store any `json:"store"`

	// URL is the server's address: `rediss://10.1.2.3:6379` on gcp,
	// `redis://127.0.0.1:26379/0` locally. Its scheme is redis or rediss.
	URL any `json:"url"`

	// CA is the PEM of the server's CA, which a rediss URL is verified
	// against; set only with rediss.
	CA any `json:"ca,omitempty"`

	// Auth is how the client authenticates: MemoryStoreAuthIAM, with the
	// workload's access token as the password, or MemoryStoreAuthNone.
	Auth any `json:"auth"`

	// Member is the member's name in a member of a parameterized
	// environment, which shares its parent's server and prefixes its
	// names with `<store>@<member>`. Unset elsewhere.
	Member any `json:"member,omitempty"`
}

// The authentications of a MemoryStoreConnection.
const (
	// MemoryStoreAuthIAM authenticates each new connection with the
	// workload's OAuth access token as the password of user `default`. It
	// needs a rediss URL.
	MemoryStoreAuthIAM = "iam"

	// MemoryStoreAuthNone sends no credential.
	MemoryStoreAuthNone = "none"
)
