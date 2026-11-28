package servergen_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

// The provider caller fixture (#744): relay is a provider package that
// calls the app with its key, its slot's CALLER_KEY; relay-ts-api's config
// names it in slot RELAY and declares RELAY_CALLER_KEY, and its two routes
// need messages.relay, the relay's role, and notes.read, a person's;
// relay-ts-stack serves it.
const (
	providerCallerRoot  = "testdata/provider-caller"
	providerCallerAPI   = "relay-ts-api"
	providerCallerStack = "relay-ts-stack"
)

// providerCallerImplementation is relay-ts-api's implementation: each route
// answers whom it ran as, and its authenticator knows one person, who
// holds notes.read.
const providerCallerImplementation = `import type { AuthenticatorFactory, Constructor } from '@schemas/relay-ts-api-api';

const caller = (ctx: { principal: { subject: string; permissions: readonly string[] } | null }) => ({
  subject: ctx.principal?.subject ?? '',
  permissions: [...(ctx.principal?.permissions ?? [])],
});

export const create: Constructor = () => ({
  relayed: {
    async relay(_input, ctx) {
      return caller(ctx);
    },
    async readNotes(_input, ctx) {
      return caller(ctx);
    },
  },
});

export const authenticate: AuthenticatorFactory = () => async ctx =>
  ctx.bearerToken === 'person-token' ? { subject: 'person-1', permissions: ['notes.read'] } : null;
`

// TestATypeScriptServerTakesAProvidersCallerKey (#744): the generated
// TypeScript server of relay-ts-api wraps the implementation's
// authenticator in its API package's callerAuthenticator, outermost. A
// request bearing RELAY_CALLER_KEY runs as the relay, extension:relay, with
// its role alone: it reaches the relay's route and not a person's. Any
// other token goes on to the implementation's authenticator, and a token
// neither knows is refused. The key is made here, and the server's log
// holds none of it.
func TestATypeScriptServerTakesAProvidersCallerKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping a TypeScript server in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range []string{providerCallerAPI, providerCallerStack} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(providerCallerRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	f.build(t, repoRoot, paths, providerCallerAPI, providerCallerStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	providers, err := os.ReadFile(filepath.Join(out, "api", providerCallerAPI, "providers.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(providers), "export function callerAuthenticator(config: EnvConfig, inner: Authenticator): Authenticator") {
		t.Errorf("relay-ts-api's providers.ts has no callerAuthenticator:\n%s", providers)
	}
	dir := servergen.ServerDir(out, providerCallerStack, providerCallerAPI)
	main, err := os.ReadFile(filepath.Join(dir, servergen.TypeScriptMainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), ".callerAuthenticator(") {
		t.Errorf("main.ts does not wrap the implementation's authenticator in callerAuthenticator:\n%s", main)
	}
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, providerCallerAPI)
	if err := os.WriteFile(filepath.Join(impl, "index.ts"), []byte(providerCallerImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, pkg := range []string{dir, impl} {
		cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		cmd.Dir = pkg
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", pkg, err, output)
		}
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	key := "caller-" + hex.EncodeToString(raw)
	server := bunServer(t, bun, dir, "RELAY=relay", "RELAY_CALLER_KEY="+key)
	call := func(method, path, token string) (int, string, []string) {
		t.Helper()
		req, err := http.NewRequest(method, server.base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var answer struct {
			Data struct {
				Subject     string   `json:"subject"`
				Permissions []string `json:"permissions"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &answer)
		return resp.StatusCode, answer.Data.Subject, answer.Data.Permissions
	}
	for _, c := range []struct {
		name, method, path, token string
		status                    int
		subject                   string
		permissions               []string
	}{
		{"the relay's key on its route", http.MethodPost, "/api/relayed", key, http.StatusOK, "extension:relay", []string{"messages.relay"}},
		{"the relay's key on a person's route", http.MethodGet, "/api/notes", key, http.StatusForbidden, "", nil},
		{"a person's token on the person's route", http.MethodGet, "/api/notes", "person-token", http.StatusOK, "person-1", []string{"notes.read"}},
		{"a person's token on the relay's route", http.MethodPost, "/api/relayed", "person-token", http.StatusForbidden, "", nil},
		{"a token neither knows", http.MethodPost, "/api/relayed", "forged-key", http.StatusUnauthorized, "", nil},
		{"no token", http.MethodPost, "/api/relayed", "", http.StatusUnauthorized, "", nil},
	} {
		status, subject, permissions := call(c.method, c.path, c.token)
		if status != c.status || subject != c.subject || !slices.Equal(permissions, c.permissions) {
			t.Errorf("%s: %d as %q holding %v, want %d as %q holding %v\n%s", c.name, status, subject, permissions, c.status, c.subject, c.permissions, server.out.String())
		}
	}
	server.stop(t)
	if strings.Contains(server.out.String(), key) {
		t.Error("the server's log holds the relay's key")
	}
}
