package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStackInitWritesACloudflareEnvironment: the installed binary links
// the cloudflare target, so stack init writes a cloudflare environment
// from an account's id, with the types of the target's values for tsc,
// read from the schema the target registers (D64). The stack loads with
// tsc's checks.
func TestStackInitWritesACloudflareEnvironment(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	schemasRoot := filepath.Join(project, "schemas")
	copyTree(t, filepath.Join(initFixture, "services"), filepath.Join(schemasRoot, "services"))
	base, err := os.ReadFile(filepath.Join(initFixture, "tsconfig.base.json"))
	if err != nil {
		t.Fatal(err)
	}
	base = []byte(strings.ReplaceAll(string(base), "../../../../", filepath.ToSlash(repoRoot)+"/"))
	if err := os.WriteFile(filepath.Join(schemasRoot, "tsconfig.base.json"), base, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	const account = "0123456789abcdef0123456789abcdef"
	out := run(t, "stack", "init", "--cloudflare-account", account)
	for _, want := range []string{
		"  src/cloudflare-target.ts\n",
		"  Staging on cloudflare, account " + account + "\n",
		"  superschematic stack bootstrap Staging\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stack init printed no %q:\n%s", want, out)
		}
	}
	dir := filepath.Join(schemasRoot, "services", "notes-stack")
	schema, err := os.ReadFile(filepath.Join(dir, "src", "stack.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `@environment({ target: "cloudflare", cloudflare: { accountId: "` + account + `" } })` + "\nexport abstract class Staging {}\n"; !strings.Contains(string(schema), want) {
		t.Errorf("the stack declares no %q:\n%s", want, schema)
	}
	types, err := os.ReadFile(filepath.Join(dir, "src", "cloudflare-target.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"declare module \"@superschematic/stack\" {\n  interface Targets {\n    cloudflare: {\n",
		"      values: { accountId: string; workersSubdomain?: string; secretsStoreId?: string };\n",
	} {
		if !strings.Contains(string(types), want) {
			t.Errorf("src/cloudflare-target.ts holds no %q:\n%s", want, types)
		}
	}
	// The stack loads, which tsc checks. It resolves once the target has
	// platforms for the tree's servers, database and site.
	if strings.Contains(out, "error TS") {
		t.Errorf("tsc refused the stack:\n%s", out)
	}
}
