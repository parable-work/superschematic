package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The confirmation an MCP endpoint asks for before it runs a tool whose
// invocation policy is ask (D93), the TypeScript runtime's mcpconfirm.ts
// and the Rust runtime's mcp::confirm, token for token. A confirmation is
// a token the endpoint mints and redeems, statelessly but for a
// single-use claim:
//
//	v1.<mode>.<expires>.<nonce>.<binding>.<mac>
//
// mode is elicit, for the requestState of a 2026-07-28 client that asks
// the person itself, or token, for the confirmationToken argument of any
// other client; one mode's token does not redeem the other's. expires is
// the Unix second it stops being good; nonce 16 random bytes, the key the
// single-use claim takes; binding the HMAC-SHA-256 of the scope (the API's
// name), the tool, the caller and the call's exact arguments as canonical
// JSON (CanonicalJSON); mac the HMAC-SHA-256 of everything before it. Both
// are keyed with the endpoint's confirmation key, which a stack binds to
// every replica as one secret, so a token one replica minted redeems on
// another. The claim that makes it single-use is a ConfirmationStore's.

const (
	// ConfirmationArgument is the tools/call argument a confirmation token
	// comes back in, from a client that does not elicit.
	ConfirmationArgument = "confirmationToken"
	// ConfirmationInput is the key of the one input request an
	// input_required result asks an eliciting client.
	ConfirmationInput = "confirm"
	// DefaultConfirmationTTL is how long a confirmation is good for unless
	// the options say: ten minutes.
	DefaultConfirmationTTL = 600 * time.Second
	// MinConfirmationKeyBytes is the fewest bytes a confirmation key holds.
	MinConfirmationKeyBytes = 32
	// ConfirmationRequired is the problem code of a call that waits for
	// the person's confirmation, and ConfirmationDeclined of one they
	// declined.
	ConfirmationRequired = "confirmation_required"
	ConfirmationDeclined = "confirmation_declined"
	// DefaultMaxConfirmationClaims bounds a MemoryConfirmationStore.
	DefaultMaxConfirmationClaims = 100_000
)

// ConfirmationMode is how a confirmation comes back.
type ConfirmationMode string

const (
	// ModeElicit is an eliciting client's requestState.
	ModeElicit ConfirmationMode = "elicit"
	// ModeToken is a token argument.
	ModeToken ConfirmationMode = "token"
)

// ConfirmationRefusal is why a token does not redeem; empty when it does.
type ConfirmationRefusal string

const (
	RefusedInvalid  ConfirmationRefusal = "invalid"
	RefusedExpired  ConfirmationRefusal = "expired"
	RefusedMismatch ConfirmationRefusal = "mismatch"
	RefusedUsed     ConfirmationRefusal = "used"
)

// ConfirmationBinding is what a confirmation is bound to.
type ConfirmationBinding struct {
	Tool string
	// Caller is who the endpoint's auth step established (CallerOf);
	// empty for no one.
	Caller string
	// Args are the arguments without the confirmation token's.
	Args map[string]any
}

// ConfirmationToken is a minted confirmation.
type ConfirmationToken struct {
	Token string
	// ExpiresAt is when it stops being good, as RFC 3339 UTC.
	ExpiresAt string
}

// ConfirmationStore is where a token's nonce is claimed, so each token
// redeems once. A deployment with several replicas supplies one they
// share for a token to be single-use across them.
type ConfirmationStore interface {
	// Claim takes id until now + ttl unless it is taken: true when this
	// call took it.
	Claim(ctx context.Context, id string, ttl time.Duration, now time.Time) (bool, error)
}

// MemoryConfirmationStore claims in process memory. Past its bound,
// expired claims are dropped, and while it is full of live ones a claim is
// refused rather than one forgotten, which would let its token redeem
// again.
type MemoryConfirmationStore struct {
	mu        sync.Mutex
	claims    map[string]time.Time
	maxClaims int
}

// NewMemoryConfirmationStore is a store of at most maxClaims claims;
// DefaultMaxConfirmationClaims for 0.
func NewMemoryConfirmationStore(maxClaims int) *MemoryConfirmationStore {
	if maxClaims <= 0 {
		maxClaims = DefaultMaxConfirmationClaims
	}
	return &MemoryConfirmationStore{claims: map[string]time.Time{}, maxClaims: maxClaims}
}

// Claim implements ConfirmationStore.
func (s *MemoryConfirmationStore) Claim(_ context.Context, id string, ttl time.Duration, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, held := s.claims[id]
	if held && until.After(now) {
		return false, nil
	}
	if !held && len(s.claims) >= s.maxClaims {
		for claimed, end := range s.claims {
			if !end.After(now) {
				delete(s.claims, claimed)
			}
		}
		if len(s.claims) >= s.maxClaims {
			return false, nil
		}
	}
	s.claims[id] = now.Add(ttl)
	return true, nil
}

// ConfirmationOptions are what NewConfirmations takes.
type ConfirmationOptions struct {
	// Key is the confirmation key: at least MinConfirmationKeyBytes.
	Key []byte
	// Scope is what a token is good for beside its tool, the API's name.
	Scope string
	// TTL is how long a confirmation is good for, whole seconds;
	// DefaultConfirmationTTL for 0.
	TTL time.Duration
	// Store is where nonces are claimed; a MemoryConfirmationStore unless
	// given.
	Store ConfirmationStore
	// Now is the clock, for tests.
	Now func() time.Time
}

// Confirmations mints and redeems the confirmations of an MCP endpoint
// (D93).
type Confirmations struct {
	key   []byte
	scope string
	ttl   int64
	store ConfirmationStore
	now   func() time.Time
}

const (
	macDomain     = "superschematic mcp confirmation v1\x00"
	bindingDomain = "superschematic mcp confirmation binding v1\x00"
)

var expiresPattern = regexp.MustCompile(`^[0-9]{1,12}$`)

// NewConfirmations builds the confirmations of opts; an error for a key
// shorter than MinConfirmationKeyBytes or a TTL that is no positive whole
// number of seconds.
func NewConfirmations(opts ConfirmationOptions) (*Confirmations, error) {
	if len(opts.Key) < MinConfirmationKeyBytes {
		return nil, fmt.Errorf("mcp: the confirmation key holds %d bytes; it needs at least %d, random", len(opts.Key), MinConfirmationKeyBytes)
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = DefaultConfirmationTTL
	}
	if ttl < time.Second || ttl%time.Second != 0 {
		return nil, fmt.Errorf("mcp: the confirmation TTL %s is no positive whole number of seconds", ttl)
	}
	store := opts.Store
	if store == nil {
		store = NewMemoryConfirmationStore(0)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Confirmations{key: append([]byte(nil), opts.Key...), scope: opts.Scope, ttl: int64(ttl / time.Second), store: store, now: now}, nil
}

func (c *Confirmations) mac(text string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(text))
	return m.Sum(nil)
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// bind is the binding's MAC: the scope, the tool, the caller and the
// arguments, each NUL-terminated but the last.
func (c *Confirmations) bind(binding ConfirmationBinding) string {
	args := binding.Args
	if args == nil {
		args = map[string]any{}
	}
	return encode(c.mac(bindingDomain + c.scope + "\x00" + binding.Tool + "\x00" + binding.Caller + "\x00" + CanonicalJSON(args)))
}

// Mint is a confirmation of the call binding describes, which mode
// redeems.
func (c *Confirmations) Mint(mode ConfirmationMode, binding ConfirmationBinding) (ConfirmationToken, error) {
	expires := c.now().Unix() + c.ttl
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return ConfirmationToken{}, err
	}
	head := "v1." + string(mode) + "." + strconv.FormatInt(expires, 10) + "." + encode(nonce) + "." + c.bind(binding)
	return ConfirmationToken{
		Token:     head + "." + encode(c.mac(macDomain+head)),
		ExpiresAt: time.Unix(expires, 0).UTC().Format("2006-01-02T15:04:05Z"),
	}, nil
}

// Redeem redeems token, sent back in mode for the call binding
// describes: "" when it confirms the call, which claims it, or why not.
// Only a token that confirms the call is claimed, so one sent with other
// arguments stays good for its own.
func (c *Confirmations) Redeem(ctx context.Context, token any, mode ConfirmationMode, binding ConfirmationBinding) (ConfirmationRefusal, error) {
	text, ok := token.(string)
	if !ok {
		return RefusedInvalid, nil
	}
	parts := strings.Split(text, ".")
	if len(parts) != 6 || parts[0] != "v1" || (parts[1] != string(ModeElicit) && parts[1] != string(ModeToken)) || !expiresPattern.MatchString(parts[2]) {
		return RefusedInvalid, nil
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[5])
	if err != nil || len(signature) != 32 {
		return RefusedInvalid, nil
	}
	if nonce, err := base64.RawURLEncoding.DecodeString(parts[3]); err != nil || len(nonce) != 16 {
		return RefusedInvalid, nil
	}
	head := strings.Join(parts[:5], ".")
	if !hmac.Equal(signature, c.mac(macDomain+head)) {
		return RefusedInvalid, nil
	}
	if parts[1] != string(mode) {
		return RefusedMismatch, nil
	}
	expires, _ := strconv.ParseInt(parts[2], 10, 64)
	now := c.now()
	until := time.Unix(expires, 0)
	if !until.After(now) {
		return RefusedExpired, nil
	}
	if !hmac.Equal([]byte(parts[4]), []byte(c.bind(binding))) {
		return RefusedMismatch, nil
	}
	claimed, err := c.store.Claim(ctx, parts[3], until.Sub(now), now)
	if err != nil {
		return "", err
	}
	if !claimed {
		return RefusedUsed, nil
	}
	return "", nil
}
