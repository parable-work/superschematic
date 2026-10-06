package tsrestgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

const serviceAuthAPI = "fixture-service-auth-api"

// serviceAuthFixture is fixture-service-auth-api with apigen's endpoints
// for it: @requireService with and without a user clause, @allowService
// with one, a set's clause that an operation replaces or that @publicRoute
// opens, and an Authenticated set with @allowService. Its from names
// fixture-service-caller-api, whose handle it imports without depending on
// its types.
func serviceAuthFixture(t *testing.T) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, serviceAuthAPI))
	if err != nil {
		t.Fatalf("load %s: %v", serviceAuthAPI, err)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: serviceAuthAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: serviceAuthAPI, schema: schema, endpoints: endpoints}
}

// TestWriteAPIGoldenServiceAuth pins the package for
// fixture-service-auth-api. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenServiceAuth -update
func TestWriteAPIGoldenServiceAuth(t *testing.T) {
	checkGolden(t, generateFixture(t, serviceAuthFixture(t)), serviceAuthAPI)
}

// TestGenerateServiceCallersShape: each operation's table entry carries its
// effective service clause, and only those entries do; the README's auth
// cell and the implementation's JSDoc state it.
func TestGenerateServiceCallersShape(t *testing.T) {
	output := generateFixture(t, serviceAuthFixture(t))
	if !output.HasServiceCallers {
		t.Error("HasServiceCallers = false, want true")
	}
	caller := "['fixture-service-caller-api']"
	byName := endpointsByName(output)
	for name, want := range map[string]struct{ literal, auth string }{
		"reserveStock":       {"{ mode: 'require', from: " + caller + " }", "stock.reserve and service: fixture-service-caller-api"},
		"releaseReservation": {"{ mode: 'allow', from: " + caller + " }", "stock.write or service: fixture-service-caller-api"},
		"reindexStock":       {"{ mode: 'require', from: [] }", "service: any caller"},
		"getReservation":     {"", "authenticated"},
		"syncStock":          {"{ mode: 'require', from: " + caller + " }", "service: fixture-service-caller-api"},
		"syncMyStock":        {"{ mode: 'allow', from: [] }", "authenticated or service: any caller"},
		"syncStatus":         {"", "public"},
		"listReservations":   {"{ mode: 'allow', from: " + caller + " }", "authenticated or service: fixture-service-caller-api"},
	} {
		endpoint, ok := byName[name]
		if !ok {
			t.Errorf("no endpoint %s", name)
			continue
		}
		if endpoint.ServiceLiteral != want.literal {
			t.Errorf("%s service = %q, want %q", name, endpoint.ServiceLiteral, want.literal)
		}
		if got := endpoint.AuthSummary(); got != want.auth {
			t.Errorf("%s AuthSummary() = %q, want %q", name, got, want.auth)
		}
	}
	docs := map[string]string{
		"reserveStock":       "Requires a calling service (ctx.serviceCaller) that serves fixture-service-caller-api, forwarding that principal.",
		"releaseReservation": "Or a calling service (ctx.serviceCaller) that serves fixture-service-caller-api, standing in for the principal (ctx.principal is null).",
		"reindexStock":       "Requires a calling service (ctx.serviceCaller); no end user.",
	}
	for name, want := range docs {
		lines := byName[name].DocLines
		if len(lines) == 0 || lines[len(lines)-1] != want {
			t.Errorf("%s doc lines = %q, want the last to be %q", name, lines, want)
		}
	}
}

// TestGenerateWithoutServiceCallersNamesNoServiceStep: a schema without
// the decorators renders no service clause, and its docs do not mention
// authenticateService (the goldens of the other fixtures pin the bytes).
func TestGenerateWithoutServiceCallersNamesNoServiceStep(t *testing.T) {
	output := generateFixtureAPI(t)
	if output.HasServiceCallers {
		t.Error("HasServiceCallers = true for fixture-api")
	}
	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	for _, name := range []string{"router.ts", "README.md", "interfaces.ts"} {
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, word := range []string{"service:", "authenticateService", "serviceCaller"} {
			if strings.Contains(string(data), word) {
				t.Errorf("%s mentions %q", name, word)
			}
		}
	}
}

// TestGeneratedServiceAuthRouter type-checks the generated package for
// fixture-service-auth-api and drives it under bun over HTTP with the
// runtime's serviceAuthenticator: @requireService refuses a missing caller
// (401 service_unauthorized) and an unlisted one (403 service_forbidden);
// @allowService admits a listed caller without calling authenticate and
// sends anyone else through the user clause; the service step runs after
// the rate limit and the body limit; and without authenticateService a
// route with a service clause answers 401.
func TestGeneratedServiceAuthRouter(t *testing.T) {
	tree := materializeAPI(t, serviceAuthFixture(t))
	tree.typeCheck(t)
	tree.runTest(t, "service_auth_runtime.test.ts")
}
