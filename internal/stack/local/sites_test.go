package local_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// TestSiteGolden resolves the shop with its site, shop-web, in Dev (D55):
// the site is a node of its own, which the provisioner builds once and
// serves on its port with its config, shop-api's loopback URL, and
// shop-api's server gets SHOP_API_CORS, the site's origin.
func TestSiteGolden(t *testing.T) {
	s := stacktest.WithSite(shop())
	s.Name = "site-stack"
	checkGoldenEnvironment(t, assemble(t), s, stacktest.SiteShop(), "Dev")
}

// TestSiteWiring reads what the local site platform and connector derive:
// the site's origin on its own port, the public address of shop-api on
// loopback in its config, and the origin in shop-api's CORS field, one
// variable of its process.
func TestSiteWiring(t *testing.T) {
	reg := assemble(t)
	dev := resolve(t, reg, stacktest.WithSite(shop()), stacktest.SiteShop(), "Dev")
	env := registry.StackEnvironment{Stack: "shop-stack", Name: "Dev"}
	site, api := dev.Deployable("shop-web"), dev.Deployable("shop-api")
	origin := local.ServerURL(local.ServerPort(env, *site))
	if site.PublicAddress != origin || !site.Exposed {
		t.Fatalf("shop-web is reached at %v, exposed %v; want %s, exposed", site.PublicAddress, site.Exposed, origin)
	}
	if len(site.Bindings) != 1 || site.Bindings[0].Field != "shop-api" || !equalJSON(t, site.Bindings[0].Value, map[string]any{"url": api.PublicAddress}) {
		t.Errorf("shop-web's bindings = %+v, want shop-api's URL", site.Bindings)
	}
	var cors *ir.Binding
	for _, b := range api.Bindings {
		if b.Field == "SHOP_API_CORS" {
			cors = b
		}
	}
	if cors == nil || cors.CORSOf != "shop-api" || !equalJSON(t, cors.Value, map[string]any{"origins": []any{origin}}) {
		t.Fatalf("shop-api's CORS field = %+v, want shop-web's origin %s", cors, origin)
	}
	var node *ir.Resource
	for _, res := range dev.Resources.Resources {
		if res.ID == "shop-api.process" {
			node = res
		}
	}
	if node == nil || !strings.Contains(string(mustJSON(t, node.Properties["env"])), `{"name":"SHOP_API_CORS_ORIGINS","value":"`+origin+`"}`) {
		t.Errorf("shop-api's process does not set SHOP_API_CORS_ORIGINS to %s: %v", origin, node)
	}
	for _, step := range dev.DeployOrder {
		if step.Step == ir.StepRollout && strings.Contains(strings.Join(step.Resources, ","), "shop-web.site") && step.Wave < 2 {
			t.Errorf("shop-web rolls out in wave %d, before shop-api, which it calls", step.Wave)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestSiteHandler serves a site's build: each file, the directory's
// index.html, the config uncached, the fallback for a path that names no
// file, and nothing above the root.
func TestSiteHandler(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<main>shop</main>")
	write("app-1234.js", "console.log(1)")
	write("docs/index.html", "<main>docs</main>")
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"apis":{"shop-api":{"url":"http://127.0.0.1:20000"}}}`)
	for _, tc := range []struct {
		name, method, path string
		fallback           string
		status             int
		body, cache        string
	}{
		{"the index", "GET", "/", "", 200, "<main>shop</main>", "no-cache"},
		{"a script", "GET", "/app-1234.js", "", 200, "console.log(1)", ""},
		{"a directory's index", "GET", "/docs/", "", 200, "<main>docs</main>", "no-cache"},
		{"the config", "GET", ir.SiteConfigPath, "", 200, string(config), "no-store"},
		{"a route of the application", "GET", "/products/42", "index.html", 200, "<main>shop</main>", "no-cache"},
		{"a route with no fallback", "GET", "/products/42", "", 404, "", ""},
		{"a path above the root", "GET", "/../secret.txt", "", 404, "", ""},
		{"a write", "POST", "/", "index.html", 405, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, "http://127.0.0.1"+tc.path, nil)
			r.URL.Path = tc.path
			local.SiteHandler(root, tc.fallback, config).ServeHTTP(w, r)
			body, _ := io.ReadAll(w.Result().Body)
			if w.Code != tc.status || (tc.body != "" && string(body) != tc.body) {
				t.Fatalf("%s %s answered %d %q; want %d %q", tc.method, tc.path, w.Code, body, tc.status, tc.body)
			}
			if got := w.Header().Get("Cache-Control"); tc.status == http.StatusOK && got != tc.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.cache)
			}
		})
	}
}
