package local

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestABuiltSiteServesItsOriginBehindItsProxy builds a site with an origin
// as stack dev builds one with --build-sites (D91): the provisioner runs
// its build, then serves its port from the proxy in front of the build's
// files. The config names shop-api, of its origin, on the site's own
// origin and shop-orders, outside it, at its own URL; the build's page and
// shop-api's routes share the origin; a cookie login reaches shop-api with
// the browser's Host, so the standard library's cross-origin check, the
// identity runtime's with no trusted origin, passes whether the browser
// sends Sec-Fetch-Site or only Origin; the session cookie, host-only,
// comes back on the next call; and shop-orders' path is the build's, 404.
func TestABuiltSiteServesItsOriginBehindItsProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: a site's build runs on Bun")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skipf("bun is not available: %v", err)
	}
	repo := t.TempDir()
	outputRoot := filepath.Join(repo, "schemas", "dist")
	siteDir := filepath.Join(repo, "web", "shop-web")
	for name, content := range map[string]string{
		filepath.Join(outputRoot, "package.json"): `{"name": "workspace", "private": true, "workspaces": []}` + "\n",
		filepath.Join(siteDir, "package.json"):    `{"name": "shop-web-site", "private": true, "scripts": {"build": "mkdir -p dist && printf '<title>acme shop</title>' > dist/index.html"}}` + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu   sync.Mutex
		seen *http.Request
	)
	last := func() *http.Request {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
	cop := http.NewCrossOriginProtection()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Clone(r.Context())
		mu.Unlock()
		switch r.URL.Path {
		case "/auth/login":
			if err := cop.Check(r); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "tok-1", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			w.WriteHeader(http.StatusNoContent)
		case "/me":
			if c, err := r.Cookie("session"); err != nil || c.Value != "tok-1" {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"name":"Ada"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)

	site := &Site{
		ID: "shop-web.site", Deployable: "shop-web", Name: "shop-web",
		Dir: "web/shop-web", Build: "build", Output: "dist",
		Port:   freeLoopbackPort(t),
		Origin: map[string]string{"shop-api": api.URL},
	}
	site.URL = ServerURL(site.Port)
	site.Config = map[string]any{"apis": map[string]any{
		"shop-api":    map[string]any{"url": site.URL + "/api/shop-api"},
		"shop-orders": map[string]any{"url": "http://127.0.0.1:9"},
	}}
	out := &syncBuffer{}
	p := &Provisioner{Out: out, ReadyTimeout: time.Minute}
	env := &ir.ResolvedEnvironment{Stack: "site-stack", Environment: "Dev"}
	req := registry.ProvisionRequest{Environment: env, OutputRoot: outputRoot, RepositoryRoot: repo}
	if err := p.startSites(context.Background(), req, []*Site{site}); err != nil {
		t.Fatalf("start the site: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = p.stopSites(key(env)) })
	if want := fmt.Sprintf("serve site shop-web at %s from %s, and shop-api under /api/<service>", site.URL, filepath.Join(siteDir, "dist")); !strings.Contains(out.String(), want) {
		t.Fatalf("stack dev printed no %q:\n%s", want, out)
	}

	if got := httpGet(t, site.URL+"/"); got != "<title>acme shop</title>" {
		t.Fatalf("the build's page through the proxy = %q", got)
	}
	want := `{"apis":{"shop-api":{"url":"` + site.URL + `/api/shop-api"},"shop-orders":{"url":"http://127.0.0.1:9"}}}`
	if got := httpGet(t, site.URL+ir.SiteConfigPath); got != want {
		t.Fatalf("the config = %s, want %s", got, want)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar}
	for _, fetchSite := range []string{"", "same-origin"} {
		login, err := http.NewRequest(http.MethodPost, site.URL+"/api/shop-api/auth/login", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		login.Header.Set("Origin", site.URL)
		if fetchSite != "" {
			login.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		resp, err := browser.Do(login)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("a cookie login through the origin, Sec-Fetch-Site %q: %d %s, want 204", fetchSite, resp.StatusCode, body)
		}
		if setCookie := resp.Header.Get("Set-Cookie"); strings.Contains(strings.ToLower(setCookie), "domain=") {
			t.Fatalf("the session cookie names a domain: %s", setCookie)
		}
	}
	if seen := last(); seen.Host != strings.TrimPrefix(site.URL, "http://") || seen.Header.Get("X-Forwarded-Prefix") != "/api/shop-api" || seen.URL.Path != "/auth/login" {
		t.Fatalf("shop-api saw %s with Host %q and X-Forwarded-Prefix %q", seen.URL.Path, seen.Host, seen.Header.Get("X-Forwarded-Prefix"))
	}
	resp, err := browser.Get(site.URL + "/api/shop-api/me")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != `{"name":"Ada"}` {
		t.Fatalf("a call with the session cookie: %d %s", resp.StatusCode, body)
	}
	resp, err = http.Get(site.URL + "/api/shop-orders/orders")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("shop-orders' path, outside the origin: %d, want the build's 404", resp.StatusCode)
	}
}
