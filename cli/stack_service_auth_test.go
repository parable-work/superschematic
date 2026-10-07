package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/stack/local"
)

// serviceAuthServices is a stack of three Go servers with no database, in
// the YAML form: stock-api, whose operations have service clauses;
// orders-api, which calls stock-api; and audit-api, which calls orders-api
// and has no edge to stock-api. Dev runs it on the local target on the
// ports given.
func serviceAuthServices(stockPort, ordersPort, auditPort int) map[string]string {
	config := func(name, calls string) string {
		c := "name: " + name + "\nkind: API\noutputs:\n  types:\n    go: { enabled: true }\n  api: { enabled: true }\n  sdk:\n    go: { enabled: true }\n"
		if calls != "" {
			c += "calls:\n  - { name: " + calls + ", kind: API }\n"
		}
		return c
	}
	view := func(name string) string {
		return `  ` + name + `:
    name: ` + name + `
    role: APIView
    fields:
      - name: caller
        typeRef: { name: string }
        required: true
      - name: forwarded
        typeRef: { name: string }
        required: true
`
	}
	return map[string]string{
		"stock-api/schema.config.yaml": config("stock-api", ""),
		"stock-api/src/stock.schema.yaml": "name: stock-api\nkind: API\ntypes:\n" + view("CallerView") + `operationSets:
  - name: StockServices
    operations:
      # Any server with an edge to stock-api, and no end user.
      - name: reindex
        typeRef: { name: CallerView }
        required: true
        httpMethod: POST
        restPath: stock/reindex
        serviceCallers: { mode: require }
      # Only audit-api's server, which has no edge here.
      - name: audit
        typeRef: { name: CallerView }
        required: true
        httpMethod: POST
        restPath: stock/audit
        serviceCallers: { mode: require, from: [audit-api] }
      # An end user, or orders-api's server on its own.
      - name: release
        typeRef: { name: CallerView }
        required: true
        auth: true
        httpMethod: POST
        restPath: stock/release
        serviceCallers: { mode: allow, from: [orders-api] }
`,
		"orders-api/schema.config.yaml": config("orders-api", "stock-api"),
		"orders-api/src/orders.schema.yaml": "name: orders-api\nkind: API\ntypes:\n" + view("StockCaller") + `operationSets:
  - name: OrderMutations
    operations:
      - name: placeOrder
        typeRef: { name: StockCaller }
        required: true
        httpMethod: POST
        restPath: orders
`,
		"audit-api/schema.config.yaml": config("audit-api", "orders-api"),
		"audit-api/src/audit.schema.yaml": "name: audit-api\nkind: API\ntypes:\n" + view("AuditView") + `operationSets:
  - name: AuditQueries
    operations:
      - name: getStatus
        typeRef: { name: AuditView }
        required: true
        httpMethod: GET
        restPath: audit/status
`,
		"auth-stack/schema.config.yaml": "name: auth-stack\nkind: Stack\noutputs: {}\n",
		"auth-stack/src/stack.schema.yaml": fmt.Sprintf(`kind: Stack
types:
  Auth:
    name: Auth
    role: EmbeddedStruct
    stack:
      deploy:
        - { name: stock-api, kind: API }
        - { name: orders-api, kind: API }
        - { name: audit-api, kind: API }
  Dev:
    name: Dev
    role: EmbeddedStruct
    environment:
      target: local
      settings:
        - of: { service: { name: stock-api, kind: API } }
          values: { port: %d }
        - of: { service: { name: orders-api, kind: API } }
          values: { port: %d }
        - of: { service: { name: audit-api, kind: API } }
          values: { port: %d }
`, stockPort, ordersPort, auditPort),
	}
}

// The implementations the test gives stock-api and orders-api in place of
// their scaffolds: each stock-api operation answers the service caller and
// the forwarded end user its request carried, and orders-api's placeOrder
// calls stock-api's reindex on its request's context and answers what
// stock-api saw.
const (
	stockImplementation = `// Package stockapi answers what each request carried.
package stockapi

import (
	"context"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"

	api "example.com/schemas/api/stock-api"
	types "example.com/schemas/types/go/stock-api"
)

func New(api.Deps) (api.Implementations, error) {
	return api.Implementations{StockServices: &StockServices{}}, nil
}

type StockServices struct{}

func seen(ctx context.Context) (*types.CallerView, error) {
	view := &types.CallerView{}
	if caller, ok := serviceauth.CallerFromContext(ctx); ok {
		view.Caller = caller.Deployable
	}
	view.Forwarded, _ = serviceauth.ForwardedToken(ctx)
	return view, nil
}

func (*StockServices) Audit(ctx context.Context) (*types.CallerView, error)   { return seen(ctx) }
func (*StockServices) Reindex(ctx context.Context) (*types.CallerView, error) { return seen(ctx) }
func (*StockServices) Release(ctx context.Context) (*types.CallerView, error) { return seen(ctx) }
`
	ordersImplementation = `// Package ordersapi calls stock-api for the request's end user.
package ordersapi

import (
	"context"

	api "example.com/schemas/api/orders-api"
	types "example.com/schemas/types/go/orders-api"
)

func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{Order: &Order{deps: deps}}, nil
}

type Order struct{ deps api.Deps }

func (impl *Order) PlaceOrder(ctx context.Context) (*types.StockCaller, error) {
	seen, err := impl.deps.StockApi.StockServicesNamespace.Reindex(ctx)
	if err != nil {
		return nil, err
	}
	return &types.StockCaller{Caller: seen.Caller, Forwarded: seen.Forwarded}, nil
}
`
)

// edgeToken signs the token a server's signed-token source sends (D37)
// with the private key of the key pair node id, which stack dev generated
// into the environment's state directory: a compact JWS whose iss and sub
// are caller and whose aud is audience.
func edgeToken(t *testing.T, stateDir, id, caller, audience string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, local.KeysDir, id+".jwk"))
	require.NoError(t, err)
	var key local.JWK
	require.NoError(t, json.Unmarshal(data, &key))
	seed, err := base64.RawURLEncoding.DecodeString(key.D)
	require.NoError(t, err)
	header, err := json.Marshal(map[string]string{"alg": local.TokenAlgorithm, "kid": key.Kid, "typ": "JWT"})
	require.NoError(t, err)
	now := time.Now().Unix()
	claims, err := json.Marshal(map[string]any{"iss": caller, "sub": caller, "aud": audience, "iat": now, "exp": now + local.SignedTokenLifetime})
	require.NoError(t, err)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(input)))
}

// TestStackDevVerifiesServiceCallers runs `stack dev` for real on a stack
// whose stock-api has service clauses (D37). The stack has no database, so
// no container runs. The build writes stock-api's entrypoint with its
// serviceauth.go, the local connector gives stock-api orders-api's public
// key in STOCK_API_CALLERS, and the entrypoint verifies against it:
//
//   - orders-api's server calls stock-api for an end user: the call is
//     admitted as orders-api, and stock-api sees the end user's token,
//     which the call forwards beside the service credential;
//   - a request with no service credential, or with a token signed by
//     audit-api, which has no edge to stock-api, is refused 401
//     service_unauthorized, as is orders-api's token for another
//     audience;
//   - orders-api's token on a route whose from lists only audit-api is
//     refused 403 service_forbidden;
//   - on an @allowService route, orders-api stands in for the end user,
//     and a request with no service credential goes to the end-user step.
func TestStackDevVerifiesServiceCallers(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: stack dev builds and runs three servers")
	}
	repo := t.TempDir()
	schemasRoot := filepath.Join(repo, "schemas")
	servicesRoot := filepath.Join(schemasRoot, "services")
	stockPort, ordersPort, auditPort := freeTCPPort(t), freeTCPPort(t), freeTCPPort(t)
	writeFiles(t, servicesRoot, serviceAuthServices(stockPort, ordersPort, auditPort))
	writeRuntimePaths(t, schemasRoot)
	outputRoot := t.TempDir()

	// A first build scaffolds each implementation and its module; the test
	// replaces two of them, which no later build rewrites.
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outputRoot})
	require.NoError(t, root.Execute(), buf.String())
	writeFiles(t, repo, map[string]string{
		"go/stock-api/implementation.go":  stockImplementation,
		"go/orders-api/implementation.go": ordersImplementation,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	root = New(Config{})
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"stack", "dev", filepath.Join(servicesRoot, "auth-stack"), "--out", outputRoot})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	deadline := time.Now().Add(4 * time.Minute)
	for !strings.Contains(out.String(), "is running:") {
		select {
		case err := <-done:
			t.Fatalf("stack dev returned before the servers ran: %v\n%s", err, out)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the servers did not come up:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.FileExists(t, filepath.Join(outputRoot, "server", "auth-stack", "stock-api", "serviceauth.go"))

	stateDir, err := local.EnsureStateDir(schemasRoot, "auth-stack", "Dev")
	require.NoError(t, err)
	orders := edgeToken(t, stateDir, "orders-api.calls.stock-api.key", "orders-api", "stock-api")
	audit := edgeToken(t, stateDir, "audit-api.calls.orders-api.key", "audit-api", "stock-api")
	wrongAudience := edgeToken(t, stateDir, "orders-api.calls.stock-api.key", "orders-api", "orders-api")

	post := func(port int, path string, headers map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, local.ServerURL(port)+path, strings.NewReader("{}"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}
	service := func(token string) map[string]string {
		return map[string]string{"Service-Authorization": "Bearer " + token}
	}
	for _, tc := range []struct {
		name    string
		port    int
		path    string
		headers map[string]string
		status  int
		body    string
	}{
		{"orders-api calls for an end user", ordersPort, "/api/orders", map[string]string{"Authorization": "Bearer user-token-1"},
			http.StatusOK, `"caller":"orders-api","forwarded":"user-token-1"`},
		{"orders-api calls with no end user", ordersPort, "/api/orders", nil,
			http.StatusOK, `"caller":"orders-api","forwarded":""`},
		{"no service credential", stockPort, "/api/stock/reindex", nil,
			http.StatusUnauthorized, `"detail":"Service credential required","code":"service_unauthorized"`},
		{"a server with no edge", stockPort, "/api/stock/reindex", service(audit),
			http.StatusUnauthorized, `"detail":"Invalid service credential","code":"service_unauthorized"`},
		{"a token for another audience", stockPort, "/api/stock/reindex", service(wrongAudience),
			http.StatusUnauthorized, `"code":"service_unauthorized"`},
		{"a caller the route does not list", stockPort, "/api/stock/audit", service(orders),
			http.StatusForbidden, `"detail":"Service not permitted","code":"service_forbidden"`},
		{"orders-api stands in for the end user", stockPort, "/api/stock/release", service(orders),
			http.StatusOK, `"caller":"orders-api"`},
		{"an end user on the @allowService route", stockPort, "/api/stock/release", map[string]string{"Authorization": "Bearer user-token-2"},
			http.StatusOK, `"caller":"","forwarded":"user-token-2"`},
	} {
		status, body := post(tc.port, tc.path, tc.headers)
		require.Equal(t, tc.status, status, "%s: %s\n%s", tc.name, body, out)
		require.Contains(t, body, tc.body, tc.name)
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, out.String())
	case <-time.After(time.Minute):
		t.Fatalf("stack dev did not stop:\n%s", out)
	}
	t.Log(out.String())
}
