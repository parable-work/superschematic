package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// The config's defaults.
const (
	DefaultSessionTTLSeconds    int64 = 14 * 24 * 60 * 60
	DefaultTouchIntervalSeconds int64 = 60
	DefaultSameSite                   = "Lax"

	DefaultArgon2MemoryKiB   uint32 = 19456
	DefaultArgon2Iterations  uint32 = 2
	DefaultArgon2Parallelism uint32 = 1
)

// The session cookie's names: __Host-session by default, __Secure-session
// when the cookie names a domain, and session when it is not Secure, as on
// the local target over plain HTTP.
const (
	HostCookieName   = "__Host-session"
	SecureCookieName = "__Secure-session"
	PlainCookieName  = "session"
)

// maxArgon2MemoryKiB bounds the memory a config may ask of every login: 4 GiB.
const maxArgon2MemoryKiB = 1 << 22

// Config is the identity runtime's configuration, the JSON the Go,
// TypeScript and Rust runtimes read the same way. A member left out takes
// its default; WithDefaults fills them in. ParseConfig is the strict
// reader: it refuses unknown members and anything Validate refuses.
type Config struct {
	// SessionTTLSeconds is how long a session lasts after login. Nil means
	// 1209600 (14 days).
	SessionTTLSeconds *int64 `json:"sessionTtlSeconds,omitempty"`
	// IdleTimeoutSeconds ends a session not seen for this long. Nil or 0
	// means no idle timeout.
	IdleTimeoutSeconds *int64 `json:"idleTimeoutSeconds,omitempty"`
	// TouchIntervalSeconds is how often a request writes the session's
	// lastSeenAt, at most. Nil means 60; 0 writes it on every request.
	TouchIntervalSeconds *int64 `json:"touchIntervalSeconds,omitempty"`
	// Cookie configures the session cookie a cookie login sets.
	Cookie CookieConfig `json:"cookie"`
	// TrustedOrigins are the origins (scheme://host[:port]) a cookie
	// request may come from across origins, and the only ones the CORS
	// middleware answers.
	TrustedOrigins []string `json:"trustedOrigins"`
	// Password configures the password hash.
	Password PasswordConfig `json:"password"`
}

// CookieConfig is the session cookie's configuration.
type CookieConfig struct {
	// Name overrides the cookie's name. Empty means the name the other
	// members give (CookieName).
	Name string `json:"name,omitempty"`
	// Domain, when set, is the cookie's Domain attribute, so APIs on sibling
	// hosts share the session.
	Domain string `json:"domain,omitempty"`
	// Secure is the Secure attribute. Nil means true; false is for the
	// local target, served over plain HTTP.
	Secure *bool `json:"secure,omitempty"`
	// SameSite is Lax, Strict or None. Empty means Lax.
	SameSite string `json:"sameSite,omitempty"`
}

// PasswordConfig configures how passwords are hashed.
type PasswordConfig struct {
	Argon2 Argon2Config `json:"argon2"`
}

// Argon2Config is argon2id's cost. Nil members take the defaults: 19456 KiB
// (19 MiB), 2 iterations and 1 lane.
type Argon2Config struct {
	MemoryKiB   *uint32 `json:"memoryKiB,omitempty"`
	Iterations  *uint32 `json:"iterations,omitempty"`
	Parallelism *uint32 `json:"parallelism,omitempty"`
}

// ParseConfig reads a config from its JSON. It refuses unknown members,
// trailing data and anything Validate refuses, and returns the config with
// its defaults filled in.
func ParseConfig(data []byte) (Config, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("identity: config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("identity: config: trailing data after the config")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg.WithDefaults(), nil
}

// Validate refuses a config no deployment means: a session that never
// lasts, an idle timeout the touch interval outlasts, a cookie name or
// SameSite a browser would refuse, an origin that is not scheme://host, and
// an argon2 cost the algorithm does not take.
func (c Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if c.SessionTTLSeconds != nil && *c.SessionTTLSeconds <= 0 {
		add("sessionTtlSeconds must be positive, not %d", *c.SessionTTLSeconds)
	}
	if c.IdleTimeoutSeconds != nil && *c.IdleTimeoutSeconds < 0 {
		add("idleTimeoutSeconds must not be negative, not %d", *c.IdleTimeoutSeconds)
	}
	if c.TouchIntervalSeconds != nil && *c.TouchIntervalSeconds < 0 {
		add("touchIntervalSeconds must not be negative, not %d", *c.TouchIntervalSeconds)
	}
	if idle, touch := c.idleTimeoutSeconds(), c.touchIntervalSeconds(); idle > 0 && touch >= idle {
		add("touchIntervalSeconds (%d) must be less than idleTimeoutSeconds (%d), or a session idles out between two writes of lastSeenAt", touch, idle)
	}

	secure := c.cookieSecure()
	switch c.Cookie.SameSite {
	case "", "Lax", "Strict":
	case "None":
		if !secure {
			add("cookie.sameSite None needs cookie.secure, which browsers require of it")
		}
	default:
		add("cookie.sameSite must be Lax, Strict or None, not %q", c.Cookie.SameSite)
	}
	if c.Cookie.Domain != "" && !isCookieDomain(c.Cookie.Domain) {
		add("cookie.domain %q is not a domain name (letters, digits and hyphens in dot-separated labels, with no leading dot, port or scheme)", c.Cookie.Domain)
	}
	if name := c.Cookie.Name; name != "" {
		switch {
		case !isCookieName(name):
			add("cookie.name %q is not a cookie name", name)
		case strings.HasPrefix(name, "__Host-") && (!secure || c.Cookie.Domain != ""):
			add("cookie.name %q has the __Host- prefix, which needs cookie.secure and no cookie.domain", name)
		case strings.HasPrefix(name, "__Secure-") && !secure:
			add("cookie.name %q has the __Secure- prefix, which needs cookie.secure", name)
		}
	}

	for i, origin := range c.TrustedOrigins {
		if err := checkOrigin(origin); err != nil {
			add("trustedOrigins[%d]: %v", i, err)
		}
	}

	a := c.argon2Params()
	if a.Iterations < 1 {
		add("password.argon2.iterations must be at least 1")
	}
	if a.Parallelism < 1 || a.Parallelism > 255 {
		add("password.argon2.parallelism must be 1 to 255, not %d", a.Parallelism)
	}
	if a.MemoryKiB < 8*a.Parallelism {
		add("password.argon2.memoryKiB must be at least 8 times parallelism (%d), not %d", 8*a.Parallelism, a.MemoryKiB)
	}
	if a.MemoryKiB > maxArgon2MemoryKiB {
		add("password.argon2.memoryKiB must be at most %d (4 GiB), not %d", maxArgon2MemoryKiB, a.MemoryKiB)
	}

	if len(problems) > 0 {
		return fmt.Errorf("identity: config: %s", strings.Join(problems, "; "))
	}
	return nil
}

// WithDefaults returns the config with every member that takes a default
// set to its value, the cookie's name included, so it encodes as the
// config the runtime runs with.
func (c Config) WithDefaults() Config {
	out := c
	ttl, idle, touch := c.sessionTTLSeconds(), c.idleTimeoutSeconds(), c.touchIntervalSeconds()
	out.SessionTTLSeconds, out.IdleTimeoutSeconds, out.TouchIntervalSeconds = &ttl, &idle, &touch
	secure := c.cookieSecure()
	out.Cookie = CookieConfig{Name: c.CookieName(), Domain: c.Cookie.Domain, Secure: &secure, SameSite: c.cookieSameSite()}
	out.TrustedOrigins = append([]string{}, c.TrustedOrigins...)
	a := c.argon2Params()
	out.Password.Argon2 = Argon2Config{MemoryKiB: &a.MemoryKiB, Iterations: &a.Iterations, Parallelism: &a.Parallelism}
	return out
}

// SessionTTL is how long a session lasts after login.
func (c Config) SessionTTL() time.Duration {
	return time.Duration(c.sessionTTLSeconds()) * time.Second
}

// IdleTimeout is how long a session may go unseen, or 0 for no limit.
func (c Config) IdleTimeout() time.Duration {
	return time.Duration(c.idleTimeoutSeconds()) * time.Second
}

// TouchInterval is how often a request writes a session's lastSeenAt, at
// most.
func (c Config) TouchInterval() time.Duration {
	return time.Duration(c.touchIntervalSeconds()) * time.Second
}

// CookieName is the session cookie's name: cookie.name when set, otherwise
// session when the cookie is not Secure, __Secure-session when it names a
// domain, and __Host-session.
func (c Config) CookieName() string {
	switch {
	case c.Cookie.Name != "":
		return c.Cookie.Name
	case !c.cookieSecure():
		return PlainCookieName
	case c.Cookie.Domain != "":
		return SecureCookieName
	default:
		return HostCookieName
	}
}

// Argon2Params is the cost new password hashes are written with.
func (c Config) Argon2Params() Argon2Params {
	return c.argon2Params()
}

func (c Config) sessionTTLSeconds() int64 {
	if c.SessionTTLSeconds != nil {
		return *c.SessionTTLSeconds
	}
	return DefaultSessionTTLSeconds
}

func (c Config) idleTimeoutSeconds() int64 {
	if c.IdleTimeoutSeconds != nil {
		return *c.IdleTimeoutSeconds
	}
	return 0
}

func (c Config) touchIntervalSeconds() int64 {
	if c.TouchIntervalSeconds != nil {
		return *c.TouchIntervalSeconds
	}
	return DefaultTouchIntervalSeconds
}

func (c Config) cookieSecure() bool {
	return c.Cookie.Secure == nil || *c.Cookie.Secure
}

func (c Config) cookieSameSite() string {
	if c.Cookie.SameSite == "" {
		return DefaultSameSite
	}
	return c.Cookie.SameSite
}

func (c Config) argon2Params() Argon2Params {
	p := Argon2Params{MemoryKiB: DefaultArgon2MemoryKiB, Iterations: DefaultArgon2Iterations, Parallelism: DefaultArgon2Parallelism}
	a := c.Password.Argon2
	if a.MemoryKiB != nil {
		p.MemoryKiB = *a.MemoryKiB
	}
	if a.Iterations != nil {
		p.Iterations = *a.Iterations
	}
	if a.Parallelism != nil {
		p.Parallelism = *a.Parallelism
	}
	return p
}

// checkOrigin refuses an origin that is not scheme://host[:port], the form
// a browser's Origin header has and http.CrossOriginProtection compares
// with: no path (a trailing slash included), query, fragment or user.
func checkOrigin(origin string) error {
	u, err := url.Parse(origin)
	switch {
	case err != nil:
		return fmt.Errorf("%q is not a URL: %v", origin, err)
	case u.Scheme == "" || u.Host == "" || u.Opaque != "":
		return fmt.Errorf("%q is not scheme://host[:port]", origin)
	case u.User != nil:
		return fmt.Errorf("%q has a user, which an origin never does", origin)
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, "?#"):
		return fmt.Errorf("%q has a path, query or fragment, which an origin never does", origin)
	}
	return nil
}

// isCookieName reports whether name is an RFC 6265 cookie name: a token of
// visible ASCII without separators.
func isCookieName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte(`()<>@,;:\"/[]?={}`, c) >= 0 {
			return false
		}
	}
	return true
}

// isCookieDomain reports whether domain is a domain name a Domain attribute
// takes: dot-separated labels of letters, digits and hyphens, no label
// empty, starting or ending with a hyphen, or longer than 63 bytes.
func isCookieDomain(domain string) bool {
	if len(domain) > 253 {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
