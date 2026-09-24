package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

var projectionFixture = filepath.Join("tsreader", "testdata", "services", "fixture-projection")

// patchedProjection copies the TypeScript projection fixture next to itself
// (so its tsconfig paths still resolve), applies the replacements to the
// projection file, and returns the copy's directory.
func patchedProjection(t *testing.T, replacements ...string) string {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Dir(projectionFixture), "fixture-projection-loader-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(projectionFixture)); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "src", "app-preference.projection.schema.ts")
	source, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for i := 0; i+1 < len(replacements); i += 2 {
		if !strings.Contains(text, replacements[i]) {
			t.Fatalf("fixture has no %q to replace", replacements[i])
		}
		text = strings.Replace(text, replacements[i], replacements[i+1], 1)
	}
	if err := os.WriteFile(filename, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestProjectionVerifyFindingsReachTheTypeScriptAuthor: the resolution
// checks verify owns fail the load of a TypeScript service and name the
// projection file.
func TestProjectionVerifyFindingsReachTheTypeScriptAuthor(t *testing.T) {
	for _, tc := range []struct {
		name         string
		replacements []string
		want         string
	}{
		{"unknown alias", []string{`@column("channel.handle")`, `@column("store.handle")`}, `unknown alias "store"`},
		{"type drift", []string{`channelHandle: Identity.Slug;`, `channelHandle: Identity.Name;`}, "keeps its source column's type"},
		{"required over a left join", []string{`branchName: Nullable<Identity.Name>;`, `branchName: Identity.Name;`}, "comes through a left join"},
		{"setting without a dot", []string{`setting: "app.account_id"`, `setting: "account_id"`}, "dotted custom Postgres setting"},
		{"repeated rank", []string{`rank: ["user", "team", "app"]`, `rank: ["user", "user"]`}, `rank lists "user" twice`},
		{"join over a non-table", []string{`@join<Channel>("channel"`, `@join<AppPreference>("channel"`}, `@join table "AppPreference" is not a DB table type`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadService(patchedProjection(t, tc.replacements...))
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "app-preference.projection.schema.ts") {
				t.Fatalf("expected an error in the projection file containing %q, got: %v", tc.want, err)
			}
		})
	}
}

// requireFirstBinding is the shape of a distribution's projection policy: a
// check on the core DB kind that refuses a projection whose first where
// rule is not an unconditional, required binding of setting.
func requireFirstBinding(setting string) registry.CheckSpec {
	return registry.CheckSpec{
		Name:      "policy.projectionScope",
		Extension: "policy",
		Kinds:     []string{string(ir.SchemaKindDB)},
		Verify: func(schema *ir.Schema, r registry.VerifyReporter) {
			for _, td := range schema.Projections() {
				rules := td.Projection.Predicates
				if len(rules) == 0 || !rules[0].IsBinding() || rules[0].Optional || rules[0].When != nil || rules[0].Setting != setting {
					r.Errorf(td.Owner, "%s: the first where rule must bind %s", td.Name, setting)
				}
			}
		},
	}
}

type policyExtension struct{ setting string }

func (policyExtension) Name() string { return "policy" }
func (p policyExtension) Register(r *registry.Registry) error {
	return r.RegisterCheck(requireFirstBinding(p.setting))
}

// TestProjectionPolicyIsAnExtensionCheck: the core loads a projection with
// or without any particular row rule; an extension that requires one
// registers a check on the DB kind and the same load fails. No core file
// knows the setting.
func TestProjectionPolicyIsAnExtensionCheck(t *testing.T) {
	withoutScope := patchedProjection(t, `{ column: "base.account", setting: "app.account_id" },`, ``)
	if _, err := LoadService(withoutScope); err != nil {
		t.Fatalf("the core must not require a row rule: %v", err)
	}

	reg := registry.New(naming.Default())
	if err := reg.Use(policyExtension{setting: "app.account_id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadService(projectionFixture, WithRegistry(reg)); err != nil {
		t.Fatalf("a projection that satisfies the policy failed: %v", err)
	}
	_, err := LoadService(withoutScope, WithRegistry(reg))
	if err == nil || !strings.Contains(err.Error(), "AppPreference: the first where rule must bind app.account_id") {
		t.Fatalf("want the policy finding, got: %v", err)
	}

	optional := patchedProjection(t, `{ column: "base.account", setting: "app.account_id" },`, `{ column: "base.account", setting: "app.account_id", optional: true },`)
	if _, err := LoadService(optional, WithRegistry(reg)); err == nil {
		t.Fatal("an optional scope binding passed the policy")
	}
}
