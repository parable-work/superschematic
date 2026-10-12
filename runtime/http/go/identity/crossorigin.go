package identity

import (
	"fmt"
	"net/http"
	"slices"
)

// newCrossOriginProtection is the standard library's check with the
// config's trusted origins: a request with a method other than GET, HEAD
// or OPTIONS passes when its Origin is trusted; otherwise Sec-Fetch-Site
// must be same-origin or none, and without that header Origin's host must
// be the request's Host. A request with neither header passes, since no
// browser sent it. The identity runtime runs it on a request the cookie
// authenticates and on a cookie login, never on a bearer request.
func newCrossOriginProtection(trustedOrigins []string) (*http.CrossOriginProtection, error) {
	cop := http.NewCrossOriginProtection()
	for _, origin := range trustedOrigins {
		if err := cop.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("identity: trusted origin: %w", err)
		}
	}
	return cop, nil
}

// CORS is the credentialed CORS middleware for the config's trusted
// origins. A request whose Origin is trusted gets
// Access-Control-Allow-Origin (the origin) and
// Access-Control-Allow-Credentials: true, and its preflight (OPTIONS with
// Access-Control-Request-Method) is answered here with 204, the method and
// headers it asks for, and a ten-minute Max-Age. Any other request passes
// through without CORS headers, so a browser keeps a page from another
// origin from reading the answer. Every request with an Origin gets
// Vary: Origin, since the answer depends on it.
func CORS(trustedOrigins []string) func(http.Handler) http.Handler {
	trusted := slices.Clone(trustedOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Add("Vary", "Origin")
			if !slices.Contains(trusted, origin) {
				next.ServeHTTP(w, r)
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			method := r.Header.Get("Access-Control-Request-Method")
			if r.Method != http.MethodOptions || method == "" {
				next.ServeHTTP(w, r)
				return
			}
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", method)
			if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
				h.Set("Access-Control-Allow-Headers", headers)
			}
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

// CheckCrossOrigin runs the cross-origin check with cfg's trusted origins
// on r, as the service runs it on a cookie request and a cookie login: nil
// when r passes, the standard library's reason when it does not.
func CheckCrossOrigin(cfg Config, r *http.Request) error {
	cop, err := newCrossOriginProtection(cfg.TrustedOrigins)
	if err != nil {
		return err
	}
	return cop.Check(r)
}
