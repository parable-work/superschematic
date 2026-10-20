package serviceauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Loopback is a server's credential for its own calls between the APIs it
// serves (docs/stack-model.md, section 9.9). Such a call reaches the server
// on its own loopback address through the callee's SDK client, and no
// connector gives it a credential, since no edge between two deployables
// carries it. So the server makes one: when it starts, it generates an
// Ed25519 key pair that only its process holds. Each client of an API the
// server serves signs the generic connector's token with it (D63, section
// 9.2): iss, sub and aud the server's deployable, five minutes long. The
// service authenticator of each API with a service clause accepts the key
// as one more issuer beside its callers field's (Accept), whose one caller
// is the server, serving every API it serves, as a caller on another server
// is the deployable and every API it serves.
//
// No other process holds the key, so nothing outside the process can sign
// as the server, and the key dies with the process: a token an earlier
// process, or another instance of the server, signed does not verify. The
// token goes only to a loopback URL (CredentialFor), so it never leaves the
// host. Nothing is configured or stored, and nothing rotates the key but a
// restart.
type Loopback struct {
	issuer IssuerConfig
	source TokenSource
}

// LoopbackLifetimeSeconds is the longest a loopback token lives: SignedToken
// signs it for 300 seconds, and the loopback issuer refuses a longer one.
const LoopbackLifetimeSeconds = signedTokenLifetime

// NewLoopback generates the loopback key of the server named deployable,
// which serves the APIs in serves. It reads WithRandom, the source of the
// key's 32-byte seed and then of each token's jti (crypto/rand by
// default), and WithClock, the clock of the token source.
func NewLoopback(deployable string, serves []string, opts ...Option) (*Loopback, error) {
	if deployable == "" {
		return nil, errors.New("serviceauth: a loopback key needs the server's deployable")
	}
	o := buildOptions(opts)
	random := o.random
	if random == nil {
		random = rand.Reader
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(random, seed); err != nil {
		return nil, fmt.Errorf("serviceauth: loopback key: %w", err)
	}
	key := ed25519.NewKeyFromSeed(seed)
	public, err := ed25519JWK(key.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	private := public
	private.D = base64.RawURLEncoding.EncodeToString(seed)
	privateJSON, err := json.Marshal(private)
	if err != nil {
		return nil, err
	}
	source, err := SignedToken(privateJSON, deployable, deployable, deployable, opts...)
	if err != nil {
		return nil, err
	}
	return &Loopback{
		issuer: IssuerConfig{
			Issuer:             deployable,
			Audience:           deployable,
			Algorithms:         []string{AlgEdDSA},
			Keys:               []JWK{public},
			SubjectClaim:       "sub",
			MaxLifetimeSeconds: LoopbackLifetimeSeconds,
			Callers:            map[string]CallerConfig{deployable: {Deployable: deployable, Serves: slices.Clone(serves)}},
		},
		source: source,
	}, nil
}

// Issuer is the loopback issuer, the entry Accept adds to a callers field:
// the server's deployable as issuer, audience and the one caller, named
// in sub, the loopback key's public half as the one key, EdDSA, and 300
// seconds at most.
func (l *Loopback) Issuer() IssuerConfig {
	issuer := l.issuer
	issuer.Algorithms = slices.Clone(l.issuer.Algorithms)
	issuer.Keys = slices.Clone(l.issuer.Keys)
	issuer.Callers = map[string]CallerConfig{}
	for subject, c := range l.issuer.Callers {
		issuer.Callers[subject] = CallerConfig{Deployable: c.Deployable, Serves: slices.Clone(c.Serves)}
	}
	return issuer
}

// Accept returns cfg, an API's callers field, with the loopback issuer
// added after its own, which New then verifies like any other. It refuses a
// field that already names an issuer, or an alias, the loopback issuer's
// name: no connector writes one, since a server is no caller of itself
// through an edge, so such a field was written by hand.
func (l *Loopback) Accept(cfg Config) (Config, error) {
	name := l.issuer.Issuer
	for i, entry := range cfg.Issuers {
		if entry.Issuer == name || slices.Contains(entry.IssuerAliases, name) {
			return Config{}, fmt.Errorf("serviceauth: issuers[%d] is named %q, the server's own loopback issuer; a callers field lists the server's callers, never the server", i, name)
		}
	}
	out := cfg
	out.Issuers = append(slices.Clone(cfg.Issuers), l.Issuer())
	return out, nil
}

// TokenSource signs the loopback tokens: the generic connector's token
// (SignedToken), for the server as issuer, subject and audience. Every
// in-server client shares it.
func (l *Loopback) TokenSource() TokenSource {
	return l.source
}

// CredentialFor is the service credential an in-server client sends to
// endpointURL, the URL its edge derives, and the headers that carry it:
// the loopback token in Service-Authorization when the URL is a loopback
// URL (IsLoopbackURL), and none otherwise. An endpoint that names another
// host reaches what may not be this process, which must not see the
// server's credential.
func (l *Loopback) CredentialFor(endpointURL string) (TokenSource, []string) {
	if !IsLoopbackURL(endpointURL) {
		return nil, nil
	}
	return l.source, []string{HeaderName}
}

// IsLoopbackURL reports whether raw is an http or https URL whose host
// only this host answers, read literally, as the TypeScript and Rust
// runtimes read it (serviceauth_parity.json's loopback.urls): the scheme
// http or https in any case, "://", then a host that is localhost in any
// case, [::1], or four decimal octets from 0 to 255 whose first is 127,
// written without leading zeros; then nothing, or a colon and a port of
// decimal digits, before the path, query or fragment. A URL with user
// information, a percent-encoded or shortened host, or any other spelling
// of a loopback address is not one.
func IsLoopbackURL(raw string) bool {
	var rest string
	switch {
	case len(raw) >= 7 && strings.EqualFold(raw[:7], "http://"):
		rest = raw[7:]
	case len(raw) >= 8 && strings.EqualFold(raw[:8], "https://"):
		rest = raw[8:]
	default:
		return false
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	host, port := authority, ""
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return false
		}
		host, port = authority[:end+1], authority[end+1:]
	} else if i := strings.IndexByte(authority, ':'); i >= 0 {
		host, port = authority[:i], authority[i:]
	}
	if port != "" {
		digits := strings.TrimPrefix(port, ":")
		if digits == port || digits == "" || strings.Trim(digits, "0123456789") != "" {
			return false
		}
	}
	return strings.EqualFold(host, "localhost") || host == "[::1]" || isLoopbackIPv4(host)
}

// isLoopbackIPv4 reports whether host is four decimal octets from 0 to 255,
// without leading zeros, the first 127.
func isLoopbackIPv4(host string) bool {
	octets := strings.Split(host, ".")
	if len(octets) != 4 || octets[0] != "127" {
		return false
	}
	for _, octet := range octets {
		if octet == "" || len(octet) > 3 || strings.Trim(octet, "0123456789") != "" || (len(octet) > 1 && octet[0] == '0') {
			return false
		}
		if n, err := strconv.Atoi(octet); err != nil || n > 255 {
			return false
		}
	}
	return true
}
