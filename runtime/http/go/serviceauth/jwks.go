package serviceauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// jwksMaxAge is how long fetched keys serve before a request fetches
	// them again.
	jwksMaxAge = time.Hour
	// jwksMinInterval is the least time between two fetches of one URL,
	// whatever prompts them, so an unknown kid or a key endpoint that is
	// down does not turn each request into a fetch.
	jwksMinInterval = 60 * time.Second
	// jwksFetchTimeout bounds one fetch. It does not follow the request
	// that prompted the fetch, which others may be waiting on.
	jwksFetchTimeout = 10 * time.Second
)

// keyCache holds one JWKS URL's keys. Keys stay until a fetch replaces
// them; a failed fetch keeps what is cached. A lookup fetches when nothing
// is cached, when the keys are older than jwksMaxAge, or when the kid is
// unknown, and at most once per jwksMinInterval. One fetch runs at a time:
// a lookup whose kid is cached uses the cached key meanwhile, and one whose
// kid is not waits for the fetch.
type keyCache struct {
	url string

	mu        sync.Mutex
	keys      map[string]publicKey // nil until a fetch succeeds
	fetchedAt time.Time
	attempted time.Time
	inflight  chan struct{} // closed when the running fetch ends
}

func (c *keyCache) lookup(ctx context.Context, v *Verifier, bearerFile, kid string) (publicKey, error) {
	for {
		c.mu.Lock()
		now := v.now()
		key, known := c.keys[kid]
		if known && now.Sub(c.fetchedAt) < jwksMaxAge {
			c.mu.Unlock()
			return key, nil
		}
		if wait := c.inflight; wait != nil {
			c.mu.Unlock()
			if known {
				return key, nil
			}
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return publicKey{}, Unavailable(ctx.Err())
			}
		}
		if !c.attempted.IsZero() && now.Sub(c.attempted) < jwksMinInterval {
			keys := c.keys
			c.mu.Unlock()
			switch {
			case known:
				return key, nil
			case keys == nil:
				return publicKey{}, Unavailable(fmt.Errorf("no keys from %s", c.url))
			default:
				return publicKey{}, Invalid(fmt.Errorf("kid %q is not in %s", kid, c.url))
			}
		}
		done := make(chan struct{})
		c.attempted = now
		c.inflight = done
		c.mu.Unlock()

		keys, err := fetchKeys(ctx, v.fetch, c.url, bearerFile)

		c.mu.Lock()
		if err == nil {
			c.keys = keys
			c.fetchedAt = now
		}
		c.inflight = nil
		close(done)
		failed := err != nil && c.keys == nil
		c.mu.Unlock()
		if failed {
			return publicKey{}, Unavailable(err)
		}
		// Look again: the fetch may have brought the kid, or not.
	}
}

// fetchKeys fetches and reads a key set. Keys it cannot use (another type
// or curve, a use other than sig, no kid, a private member) are left out;
// of two keys with one kid, the first is kept.
func fetchKeys(ctx context.Context, fetch Fetcher, url, bearerFile string) (map[string]publicKey, error) {
	bearer := ""
	if bearerFile != "" {
		data, err := os.ReadFile(bearerFile)
		if err != nil {
			return nil, fmt.Errorf("jwksBearerTokenFile: %w", err)
		}
		bearer = strings.TrimSpace(string(data))
	}
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jwksFetchTimeout)
	defer cancel()
	data, err := fetch(fetchCtx, url, bearer)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	var set struct {
		Keys *[]JWK `json:"keys"`
	}
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("key set from %s: %w", url, err)
	}
	if set.Keys == nil {
		return nil, errors.New("key set from " + url + " has no keys member")
	}
	keys := map[string]publicKey{}
	for _, j := range *set.Keys {
		if j.Kid == "" || (j.Use != "" && j.Use != "sig") {
			continue
		}
		if _, dup := keys[j.Kid]; dup {
			continue
		}
		key, err := parsePublicJWK(j)
		if err != nil {
			continue
		}
		keys[j.Kid] = key
	}
	return keys, nil
}
