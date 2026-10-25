package local_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// sessionAPI stands in for an API over the core user model (D50): a cookie
// login, with the identity runtime's cross-origin check, the standard
// library's CrossOriginProtection with no trusted origin, which sets a
// SameSite=Lax session cookie; a route that needs the cookie; a redirect
// within its own routes; and a stream it writes an event at a time. It
// records each request it serves.
func sessionAPI(t *testing.T) (*httptest.Server, *requests) {
	t.Helper()
	seen := &requests{}
	cop := http.NewCrossOriginProtection()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if err := cop.Check(r); err != nil {
			http.Error(w, `{"code":"cross_origin"}`, http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "tok-1", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err != nil || c.Value != "tok-1" {
			http.Error(w, `{"code":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"name":"Ada"}`)
	})
	mux.HandleFunc("GET /api/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/me?from=old", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		// The stream stays open: a proxy that buffered the answer would
		// hold the first event back.
		<-r.Context().Done()
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.add(r)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

// requests are the requests a test server served, which its handlers and
// the test share.
type requests struct {
	mu   sync.Mutex
	list []*http.Request
}

func (q *requests) add(r *http.Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.list = append(q.list, r.Clone(r.Context()))
}

// last is the last request served.
func (q *requests) last() *http.Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.list[len(q.list)-1]
}

// devServer stands in for a site's dev server: its page at every path,
// and a WebSocket at /hmr, which echoes each line, as a hot-reload socket
// carries messages both ways. It records each request it serves.
func devServer(t *testing.T) (*httptest.Server, *requests) {
	t.Helper()
	seen := &requests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.add(r)
		if r.URL.Path == "/hmr" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, rw, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = rw.Flush()
			for {
				line, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				_, _ = rw.WriteString("echo " + line)
				_ = rw.Flush()
			}
		}
		_, _ = io.WriteString(w, "<title>shop</title>")
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestSiteProxyServesTheSiteAndItsAPIsOnOneOrigin: the proxy in front of
// a site's dev server (D82) answers the site's config with each API under
// /api/<service> on the origin the page asked for, serves the dev server's
// pages with the browser's Host, and each API's routes under its prefix,
// the prefix taken off and the query kept. A cookie login through it is
// same-origin, so the cross-origin check passes with no trusted origin, and
// the session cookie, SameSite=Lax, comes back on the next call; the same
// login sent straight to the API's own port from the site's origin is
// refused. A redirect within the API's routes keeps its prefix, a stream
// reaches the client as the API flushes it, an unknown service's path goes
// to the dev server, and an upstream that does not answer is 502.
func TestSiteProxyServesTheSiteAndItsAPIsOnOneOrigin(t *testing.T) {
	api, apiSeen := sessionAPI(t)
	dev, devSeen := devServer(t)
	proxy := httptest.NewServer(local.SiteProxy("shop-web", mustURL(t, dev.URL), map[string]*url.URL{"shop-api": mustURL(t, api.URL)}))
	t.Cleanup(proxy.Close)
	origin := proxy.URL
	host := mustURL(t, origin).Host

	get := func(client *http.Client, path string) (int, string) {
		t.Helper()
		resp, err := client.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// The config: shop-api on the page's own origin, under its prefix.
	resp, err := http.Get(origin + ir.SiteConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		APIs map[string]struct{ URL string } `json:"apis"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the config: %v, Cache-Control %q", err, resp.Header.Get("Cache-Control"))
	}
	_ = resp.Body.Close()
	if got, want := config.APIs["shop-api"].URL, origin+"/api/shop-api"; got != want || len(config.APIs) != 1 {
		t.Fatalf("the config names shop-api at %q (%d APIs), want %q", got, len(config.APIs), want)
	}

	// A page of the dev server, with the browser's Host.
	if status, body := get(http.DefaultClient, "/products/42?tab=reviews"); status != http.StatusOK || body != "<title>shop</title>" {
		t.Fatalf("a page through the proxy: %d %q", status, body)
	}
	last := devSeen.last()
	if last.Host != host || last.URL.Path != "/products/42" || last.URL.RawQuery != "tab=reviews" {
		t.Fatalf("the dev server saw Host %q, %s?%s; want %q, /products/42?tab=reviews", last.Host, last.URL.Path, last.URL.RawQuery, host)
	}

	// A cookie session through the proxy: the login is same-origin.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login, err := http.NewRequest(http.MethodPost, origin+"/api/shop-api/api/auth/login", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Origin", origin)
	login.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err = browser.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a cookie login through the proxy: %d, want 204", resp.StatusCode)
	}
	if cookies := jar.Cookies(mustURL(t, origin)); len(cookies) != 1 || cookies[0].Name != "session" {
		t.Fatalf("the browser keeps %v for the site's origin, want the session cookie", cookies)
	}
	if status, body := get(browser, "/api/shop-api/api/me"); status != http.StatusOK || body != `{"name":"Ada"}` {
		t.Fatalf("a call with the session cookie: %d %q", status, body)
	}
	served := apiSeen.last()
	if served.URL.Path != "/api/me" || served.Host != host || served.Header.Get("X-Forwarded-Prefix") != "/api/shop-api" {
		t.Fatalf("shop-api saw %s with Host %q and X-Forwarded-Prefix %q; want /api/me, %q and /api/shop-api", served.URL.Path, served.Host, served.Header.Get("X-Forwarded-Prefix"), host)
	}
	// The same login sent to shop-api's own port, as a page on the site's
	// origin would without the proxy, is cross-origin and refused.
	direct, err := http.NewRequest(http.MethodPost, api.URL+"/api/auth/login", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	direct.Header.Set("Origin", origin)
	direct.Header.Set("Sec-Fetch-Site", "same-site")
	resp, err = http.DefaultClient.Do(direct)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a cookie login straight to shop-api's port: %d, want 403", resp.StatusCode)
	}

	// A redirect within shop-api's routes keeps its prefix.
	resp, err = browser.Get(origin + "/api/shop-api/api/old")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || got != "/api/shop-api/api/me?from=old" {
		t.Fatalf("a redirect through the proxy: %d to %q, want 303 to /api/shop-api/api/me?from=old", resp.StatusCode, got)
	}

	// A stream reaches the client as shop-api flushes it.
	stream, err := http.Get(origin + "/api/shop-api/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stream.Body).ReadString('\n')
		read <- line
	}()
	select {
	case line := <-read:
		if line != "data: first\n" {
			t.Fatalf("the stream's first line: %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy held the stream's first event back")
	}
	_ = stream.Body.Close()

	// A path under /api/ that names no API the site calls is the dev
	// server's.
	if status, _ := get(http.DefaultClient, "/api/shop-orders/x"); status != http.StatusOK || devSeen.last().URL.Path != "/api/shop-orders/x" {
		t.Fatalf("an unknown service's path: %d, want the dev server's page", status)
	}

	// An upstream that does not answer is 502, named.
	dev.Close()
	if status, body := get(http.DefaultClient, "/"); status != http.StatusBadGateway || !strings.Contains(body, "site shop-web: its dev server at "+dev.URL) {
		t.Fatalf("with the dev server down: %d %q", status, body)
	}
}

// TestSiteProxyPassesWebSocketUpgrades: an upgrade to a WebSocket reaches
// the dev server through the proxy, which answers 101 and then carries the
// socket's bytes both ways, as a dev server's hot reload needs (D82).
func TestSiteProxyPassesWebSocketUpgrades(t *testing.T) {
	dev, _ := devServer(t)
	proxy := httptest.NewServer(local.SiteProxy("shop-web", mustURL(t, dev.URL), nil))
	t.Cleanup(proxy.Close)
	conn, err := net.Dial("tcp", mustURL(t, proxy.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "GET /hmr HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", mustURL(t, proxy.URL).Host)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("the upgrade through the proxy: %d, want 101", resp.StatusCode)
	}
	for _, message := range []string{"update src/App.tsx\n", "ping\n"} {
		if _, err := io.WriteString(conn, message); err != nil {
			t.Fatal(err)
		}
		line, err := r.ReadString('\n')
		if err != nil || line != "echo "+message {
			t.Fatalf("the socket carried back %q, %v; want %q", line, err, "echo "+message)
		}
	}
}
