package identity_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// originConfig is a config with no trusted origin and no cookie domain,
// the identity runtime's defaults but for a cheap password hash: what an
// API a site serves on its own origin runs with (D91).
const originConfig = `{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`

// TestCookieSessionOnASitesOrigin: an API a site serves on its own origin
// (D91) gets the browser's request as the site's load balancer or Worker,
// or stack dev's proxy, forwards it: the site's Host, the path with its
// prefix taken off and X-Forwarded-Prefix, -Host and -Proto. A cookie
// login there passes the cross-origin check with no trusted origin,
// whether the browser sends Sec-Fetch-Site same-origin or Origin alone,
// and sets the __Host- session cookie, Secure, Path=/ and with no Domain,
// so the browser keeps it for the site's host alone; a call with it is
// admitted; and no answer carries CORS headers. The same login sent to the
// API's own host from the site's page, as without the origin, is
// cross-origin and refused, which a trusted origin would have had to
// allow.
func TestCookieSessionOnASitesOrigin(t *testing.T) {
	const site = "https://shop-web.acme.dev"
	eachHarness(t, originConfig, func(t *testing.T, h *harness) {
		send := func(method, url, path string, body any, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
			t.Helper()
			var data []byte
			if body != nil {
				var err error
				if data, err = json.Marshal(body); err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest(method, url+path, bytes.NewReader(data))
			req.Header.Set("X-Forwarded-Host", req.Host)
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Prefix", "/api/shop-api")
			for i := 0; i+1 < len(headers); i += 2 {
				req.Header.Set(headers[i], headers[i+1])
			}
			rec := httptest.NewRecorder()
			h.router.ServeHTTP(rec, req)
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			return rec, out
		}
		login := map[string]any{"login": "admin@example.com", "password": adminPassword, "session": "cookie"}
		var token string
		for _, headers := range [][]string{
			{"Origin", site, "Sec-Fetch-Site", "same-origin"},
			{"Origin", site},
		} {
			rec, body := send(http.MethodPost, site, "/auth/login", login, headers...)
			if rec.Code != http.StatusOK {
				t.Fatalf("a cookie login on the site's origin with %v: %d %v", headers, rec.Code, body)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != "__Host-session" || !cookies[0].Secure || cookies[0].Path != "/" || cookies[0].Domain != "" {
				t.Fatalf("the session cookie is %v, want __Host-session, Secure, Path=/ and no Domain", cookies)
			}
			if set := rec.Header().Get("Set-Cookie"); strings.Contains(strings.ToLower(set), "domain=") {
				t.Fatalf("the session cookie names a domain: %s", set)
			}
			if allow := rec.Header().Get("Access-Control-Allow-Origin"); allow != "" {
				t.Errorf("a same-origin login answered CORS for %s", allow)
			}
			token = cookies[0].Value
		}
		rec, body := send(http.MethodPost, site, "/orders", nil, "Cookie", "__Host-session="+token, "Origin", site, "Sec-Fetch-Site", "same-origin")
		if rec.Code != http.StatusOK || rec.Body.String() != "Admin" {
			t.Fatalf("a call with the session cookie on the site's origin: %d %q %v", rec.Code, rec.Body.String(), body)
		}
		rec, body = send(http.MethodPost, "https://shop-api.acme.dev", "/auth/login", login, "Origin", site, "Sec-Fetch-Site", "same-site")
		if rec.Code != http.StatusForbidden || body["code"] != "cross_origin" {
			t.Fatalf("a cookie login to the API's own host from the site's page: %d %v, want 403 cross_origin", rec.Code, body)
		}
	})
}
