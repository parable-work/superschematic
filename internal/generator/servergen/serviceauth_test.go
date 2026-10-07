package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
