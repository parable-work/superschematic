package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAServiceClauseTakesTheServiceAuthenticator: an API whose operations
// have a service clause gets its Config's service authenticator from
// serviceAuthenticator, which serviceauth.go builds from the API's callers
// field, and a server whose APIs have none gets neither.
func TestAServiceClauseTakesTheServiceAuthenticator(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	withServiceClause(f)
	f.build(t, repoRoot, fakePaths(repoRoot), "shop-stack")
	out := filepath.Join(repoRoot, "schemas", "dist")
	read := func(server, file string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(servergen.ServerDir(out, "shop-stack", server), file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	main := read("shop-api", servergen.MainFile)
	for _, want := range []string{
		`shopApiServiceAuthenticator, err := serviceAuthenticator("shop-api")`,
		"ServiceAuthenticator: shopApiServiceAuthenticator,",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("shop-api's main.go lacks %q:\n%s", want, main)
		}
	}
	auth := read("shop-api", servergen.ServiceAuthFile)
	for _, want := range []string{
		`"shop-api": "SHOP_API_CALLERS",`,
		"func serviceAuthenticator(api string) (serviceauth.Authenticator, error) {",
		"cfg, err := stackconfig.LoadCallers(field)",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("shop-api's serviceauth.go lacks %q:\n%s", want, auth)
		}
	}
	if storefront := read("Storefront", servergen.MainFile); strings.Contains(storefront, "serviceAuthenticator") {
		t.Errorf("Storefront, whose APIs have no service clause, has a service authenticator:\n%s", storefront)
	}
	if _, err := os.Stat(filepath.Join(servergen.ServerDir(out, "shop-stack", "Storefront"), servergen.ServiceAuthFile)); err == nil {
		t.Error("Storefront, whose APIs have no service clause, has serviceauth.go")
	}
}

// TestServiceAuthEntrypointGolden: the entrypoint of shop-api with a
// service clause, main.go and serviceauth.go. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestServiceAuthEntrypointGolden -update
func TestServiceAuthEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	withServiceClause(f)
	f.build(t, repoRoot, fakePaths(repoRoot), "shop-stack")
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack", "shop-api")
	for _, file := range []string{servergen.MainFile, servergen.ServiceAuthFile} {
		got, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(goldenRoot, "service-auth", "server", "shop-stack", "shop-api", file)
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s; run with -update and review the diff", file, golden)
		}
	}
}

// signEdgeToken signs the token a caller's signed-token source sends
// (D37): a compact JWS with alg EdDSA and the key's kid, iss and sub the
// caller, aud the callee, and exp five minutes after iat.
func signEdgeToken(t *testing.T, key ed25519.PrivateKey, kid, caller, callee string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	claims, err := json.Marshal(map[string]any{"iss": caller, "sub": caller, "aud": callee, "iat": now, "exp": now + 300})
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

// callersVariables are shop-api's callers field as a local environment
// sets it, Storefront an issuer of its own with its edge's public key.
func callersVariables(t *testing.T, public ed25519.PublicKey, kid string) []string {
	t.Helper()
	jwk, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": kid, "x": base64.RawURLEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	vars, err := ir.DerivedVariables(ir.CallersField("shop-api"), ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{{
		Issuer: "Storefront", Audience: "shop-api", Algorithms: []string{ir.AlgorithmEdDSA},
		Keys:               []ir.ServiceAuthKey{{JWK: string(jwk)}},
		MaxLifetimeSeconds: 300,
		Callers:            []ir.ServiceAuthCaller{{Subject: "Storefront", Deployable: "Storefront", Serves: []string{"shop-orders", "shop-reviews"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	env := make([]string, len(vars))
	for i, v := range vars {
		env[i] = fmt.Sprintf("%s=%v", v.Name, v.Value)
	}
	return env
}

// TestAServiceClauseAdmitsItsCallers: shop-api, whose renameProduct
// Storefront's shop-orders may call on its own (@allowService), refuses to
// start without its callers field and starts with it. With the field, a
// token Storefront signs with its edge's key stands in for the end user,
// and the scaffold answers 501; a token signed with another key is 401
// service_unauthorized; a request with no service credential goes to the
// end-user step, whose scaffolded auth middleware refuses it. With no
// issuers, as where no server calls shop-api, every service credential is
// refused.
func TestAServiceClauseAdmitsItsCallers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	withServiceClause(f)
	f.build(t, repoRoot, paths)
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack", "shop-api")
	goCommand(t, dir, "mod", "tidy")
	goCommand(t, dir, "vet", ".")
	binary := filepath.Join(t.TempDir(), "shop-api")
	goCommand(t, dir, "build", "-o", binary, ".")
	database := "SHOP_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_db?connect_timeout=1&sslmode=disable"

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "PORT="+freePort(t), database)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("shop-api started without its callers field:\n%s", out)
	}
	if want := "required environment variable SHOP_API_CALLERS_ISSUERS is not set"; !strings.Contains(string(out), want) {
		t.Errorf("shop-api stopped without saying %q:\n%s", want, out)
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shopAPI := start(t, binary, append(callersVariables(t, public, "storefront-key"), database)...)
	rename, body := "/api/products/p-1/name", `{"name":"x"}`
	shopAPI.sendWith(t, http.MethodPut, rename, body, map[string]string{"Service-Authorization": "Bearer " + signEdgeToken(t, private, "storefront-key", "Storefront", "shop-api")}, http.StatusNotImplemented, "Product.RenameProduct")
	shopAPI.sendWith(t, http.MethodPut, rename, body, map[string]string{"Service-Authorization": "Bearer " + signEdgeToken(t, other, "storefront-key", "Storefront", "shop-api")}, http.StatusUnauthorized, "service_unauthorized")
	shopAPI.sendWith(t, http.MethodPut, rename, body, map[string]string{"Service-Authorization": "Bearer " + signEdgeToken(t, private, "storefront-key", "Storefront", "shop-orders")}, http.StatusUnauthorized, "service_unauthorized")
	shopAPI.sendWith(t, http.MethodPut, rename, body, nil, http.StatusUnauthorized)
	shopAPI.stop(t)

	nobody := start(t, binary, ir.CallersField("shop-api")+"_ISSUERS=0", database)
	nobody.sendWith(t, http.MethodPut, rename, body, map[string]string{"Service-Authorization": "Bearer " + signEdgeToken(t, private, "storefront-key", "Storefront", "shop-api")}, http.StatusUnauthorized, "service_unauthorized")
	nobody.stop(t)
}

// signToken is a token like signEdgeToken's, with its issuer, subject
// and audience each its own, issued at iat.
func signToken(t *testing.T, key ed25519.PrivateKey, kid, issuer, subject, audience string, iat time.Time) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := json.Marshal(map[string]any{"iss": issuer, "sub": subject, "aud": audience, "iat": iat.Unix(), "exp": iat.Unix() + 300})
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

// sendWith sends method to path with headers and the JSON body body, and
// checks the status and that the answer holds each of contains.
func (s *started) sendWith(t *testing.T, method, path, body string, headers map[string]string, status int, contains ...string) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Errorf("%s %s with %v = %d, want %d: %s", method, path, headers, resp.StatusCode, status, answer)
	}
	for _, want := range contains {
		if !strings.Contains(string(answer), want) {
			t.Errorf("%s %s answered %s, which lacks %q", method, path, answer, want)
		}
	}
}

// withRequiredService gives shop-api's getProduct a @requireService clause
// listing shop-orders, and no user clause.
func withRequiredService(f fixture) {
	for _, set := range f.schemas["shop-api"].OperationSets {
		for _, op := range set.Operations {
			if op.Name == "getProduct" {
				op.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"shop-orders"}}
			}
		}
	}
}

// loopbackCapture is a proxy on loopback in front of a server, which keeps
// the last Service-Authorization header it passed on: what the server's
// client of an API it serves itself sent it (docs/stack-model.md, section
// 9.9). Its URL is a loopback URL, so the client sends its credential
// there as it would to the server's own address.
type loopbackCapture struct {
	url  string
	mu   sync.Mutex
	last string
}

// captureLoopback starts a loopbackCapture in front of target.
func captureLoopback(t *testing.T, target string) *loopbackCapture {
	t.Helper()
	to, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	c := &loopbackCapture{}
	proxy := httputil.NewSingleHostReverseProxy(to)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.last = r.Header.Get("Service-Authorization")
		c.mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c.url = srv.URL
	return c
}

// token is the bearer token of the last Service-Authorization header the
// proxy passed on.
func (c *loopbackCapture) token(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	token, ok := strings.CutPrefix(c.last, "Bearer ")
	if !ok {
		t.Fatalf("the in-server call carried Service-Authorization %q, not a bearer token", c.last)
	}
	return token
}

// checkLoopbackCalls drives a server of two APIs, named server, whose
// calling API's operation at callerPath calls the other's @requireService
// operation at calleePath through capture: the call is admitted and the
// calling route answers 200 holding each of want, which the callee wrote
// from the caller it saw. The token the call carried is the generic
// connector's, iss, sub and aud the server, five minutes long. From
// outside, the callee's route refuses a request with no credential, and a
// token signed with another key under the server's name and the loopback
// key's kid, current or expired, with 401 service_unauthorized. It returns
// the call's token.
func checkLoopbackCalls(t *testing.T, s *started, capture *loopbackCapture, server, callerPath, calleePath string, want ...string) string {
	t.Helper()
	s.expect(t, http.MethodGet, callerPath, http.StatusOK, want...)
	token := capture.token(t)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the in-server call carried %q, not a compact JWS", token)
	}
	var header struct{ Alg, Kid string }
	var claims struct {
		Iss, Sub, Aud string
		Iat, Exp      int64
	}
	for i, into := range []any{&header, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatal(err)
		}
	}
	if header.Alg != "EdDSA" || header.Kid == "" || claims.Iss != server || claims.Sub != server || claims.Aud != server || claims.Exp-claims.Iat != 300 {
		t.Errorf("the in-server call's token is %+v %+v; want EdDSA, iss, sub and aud %s, five minutes long", header, claims, server)
	}

	s.sendWith(t, http.MethodGet, calleePath, "", nil, http.StatusUnauthorized, "service_unauthorized")
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, iat := range []time.Time{time.Now(), time.Now().Add(-time.Hour)} {
		forged := signToken(t, other, header.Kid, server, server, server, iat)
		s.sendWith(t, http.MethodGet, calleePath, "", map[string]string{"Service-Authorization": "Bearer " + forged}, http.StatusUnauthorized, "service_unauthorized")
	}
	return token
}

// TestAServerCallsAnAPIItServesWithItsLoopbackKey: Monolith serves
// shop-api, whose getProduct only shop-orders' server may call
// (@requireService, no user clause), and shop-orders, whose getOrder calls
// getProduct through its client of shop-api, at the loopback URL the local
// connector derives for a call within one server, with no credential
// (docs/stack-model.md, section 9.9). The call carries Monolith's loopback
// token, which getProduct's service authenticator admits as Monolith,
// serving both APIs, beside its callers field, which lists none here.
// From outside, getProduct refuses no credential and a forged or expired
// token; and the token the call carried does not verify with a second
// Monolith process, whose key is its own.
func TestAServerCallsAnAPIItServesWithItsLoopbackKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	withRequiredService(f)
	f.build(t, repoRoot, paths, "shop-db", "shop-media", "shop-api", "shop-orders", monolithStack)

	implement := func(service, from, to string, imports ...string) {
		t.Helper()
		file := filepath.Join(naming.Default().GoImplementationDir(repoRoot, service), apigen.ImplementationFile)
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		implemented := strings.Replace(string(source), from, to, 1)
		for _, path := range imports {
			implemented = strings.Replace(implemented, "import (\n", "import (\n\t"+strconv.Quote(path)+"\n", 1)
		}
		if implemented == string(source) {
			t.Fatalf("%s has no %q:\n%s", file, from, source)
		}
		if err := os.WriteFile(file, []byte(implemented), 0o644); err != nil {
			t.Fatal(err)
		}
		goCommand(t, filepath.Dir(file), "mod", "tidy")
	}
	implement("shop-api", `return nil, api.NotImplementedError("Product.GetProduct")`, `caller, _ := serviceauth.CallerFromContext(ctx)
	return &types.ProductView{Id: id, Name: caller.Deployable + " serving " + strings.Join(caller.Serves, " and ")}, nil`,
		"strings", "github.com/parable-work/superschematic/runtime/http/go/serviceauth")
	implement("shop-orders", `return nil, api.NotImplementedError("Order.GetOrder")`, `product, err := impl.deps.ShopApi.ProductNamespace.GetProduct(ctx, id)
	if err != nil {
		return nil, api.InternalError(err)
	}
	return &types.OrderView{Id: product.Name}, nil`)

	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), monolithStack, "Monolith")
	goCommand(t, dir, "mod", "tidy")
	goCommand(t, dir, "vet", ".")
	binary := filepath.Join(t.TempDir(), "Monolith")
	goCommand(t, dir, "build", "-o", binary, ".")

	env := append([]string{
		"SHOP_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_db?connect_timeout=1&sslmode=disable",
		ir.CallersField("shop-api") + "_ISSUERS=0",
	}, bucketVariables(t, "SHOP_MEDIA_BUCKET", "http://127.0.0.1:9")...)
	port := freePort(t)
	capture := captureLoopback(t, "http://127.0.0.1:"+port)
	monolith := startOn(t, port, binary, append(env, "SHOP_API_SERVICE_URL="+capture.url)...)
	token := checkLoopbackCalls(t, monolith, capture, "Monolith", "/api/orders/p-1", "/api/products/p-1", "Monolith serving shop-api and shop-orders")
	monolith.stop(t)

	again := start(t, binary, append(env, "SHOP_API_SERVICE_URL=http://127.0.0.1:9")...)
	again.sendWith(t, http.MethodGet, "/api/products/p-1", "", map[string]string{"Service-Authorization": "Bearer " + token}, http.StatusUnauthorized, "service_unauthorized")
	again.stop(t)
}
