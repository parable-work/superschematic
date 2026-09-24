package schemaconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func writeConfig(t *testing.T, name, contents string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// kindList is a KindSet over a fixed list; it stands in for the registry,
// which this package cannot import. coreKinds is the three kinds the core
// registers.
type kindList []string

func (k kindList) KnowsKind(kind ir.SchemaKind) bool {
	for _, name := range k {
		if name == string(kind) {
			return true
		}
	}
	return false
}

func (k kindList) Kinds() []string { return k }

var coreKinds = kindList{"API", "DB", "General"}

func TestReadFileValidJSON(t *testing.T) {
	dir := writeConfig(t, "schema.config.json", `{
		"name": "web-db",
		"kind": "DB",
		"dependencies": [{"name": "enums", "kind": "General"}],
		"outputs": {"types": {"go": {"enabled": true}}}
	}`)
	cfg, err := ReadFile(dir, coreKinds)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if cfg.Name != "web-db" || string(cfg.Kind) != "DB" {
		t.Errorf("cfg identity = %q/%q", cfg.Name, cfg.Kind)
	}
	if len(cfg.Dependencies) != 1 || cfg.Dependencies[0].Name != "enums" {
		t.Errorf("dependencies = %+v", cfg.Dependencies)
	}
}

func TestReadFileValidYAML(t *testing.T) {
	dir := writeConfig(t, "schema.config.yaml", `name: web-api
kind: API
public: true
authDb: web-db
dependencies:
  - { name: web-db, kind: DB }
outputs:
  api: { enabled: true }
  sdk:
    typescript: { enabled: true }
`)
	cfg, err := ReadFile(dir, coreKinds)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !cfg.Public || cfg.AuthDB != "web-db" {
		t.Errorf("cfg = %+v", cfg)
	}
}

// An extension generator's output key reaches the registry from a data-form
// config, as it does from schema.config.ts: the embedded schema checks the
// core sections and leaves other keys to registry.ParseOutputs, which knows
// the registered generators and their OutputSchema.
func TestReadFileAcceptsAnExtensionOutputKey(t *testing.T) {
	dir := writeConfig(t, "schema.config.yaml", `name: shop
kind: DB
outputs:
  types: { go: { enabled: true } }
  catalog: { enabled: true }
`)
	cfg, err := ReadFile(dir, coreKinds)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if _, ok := cfg.Outputs["catalog"]; !ok {
		t.Errorf("outputs = %+v, want the catalog section kept", cfg.Outputs)
	}
	bad := writeConfig(t, "schema.config.json", `{"name": "shop", "kind": "DB", "outputs": {"types": {"go": {"enabled": "yes"}}}}`)
	if _, err := ReadFile(bad, coreKinds); err == nil {
		t.Error("a malformed core section was accepted")
	}
}

func TestReadFileRejectsUnknownKeys(t *testing.T) {
	dir := writeConfig(t, "schema.config.json", `{
		"name": "web-db", "kind": "DB", "outputs": {}, "sparkles": true
	}`)
	_, err := ReadFile(dir, coreKinds)
	if err == nil {
		t.Fatal("ReadFile accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "sparkles") {
		t.Errorf("error %q does not mention the unknown key", err.Error())
	}
}

func TestReadFileRequiresOutputs(t *testing.T) {
	dir := writeConfig(t, "schema.config.json", `{"name": "web-db", "kind": "DB"}`)
	if _, err := ReadFile(dir, coreKinds); err == nil {
		t.Fatal("ReadFile accepted a config without outputs")
	}
}

func TestReadFileRejectsMalformedDependencies(t *testing.T) {
	dir := writeConfig(t, "schema.config.json", `{
		"name": "web-db", "kind": "DB", "outputs": {},
		"dependencies": [{"name": "enums"}]
	}`)
	if _, err := ReadFile(dir, coreKinds); err == nil {
		t.Fatal("ReadFile accepted a dependency without a kind")
	}

	dir = writeConfig(t, "schema.config.json", `{
		"name": "web-db", "kind": "DB", "outputs": {},
		"dependencies": [{"name": "enums", "kind": "Genral"}]
	}`)
	_, err := ReadFile(dir, coreKinds)
	if err == nil {
		t.Fatal("ReadFile accepted a dependency of a kind the set does not know")
	}
	if !strings.Contains(err.Error(), `kind "Genral"`) || !strings.Contains(err.Error(), "registered kinds: API, DB, General") {
		t.Errorf("error %q does not name the bad dependency kind and the registered kinds", err.Error())
	}
}

// TestValidateShapeRejectsUnknownKind: the JSON Schema leaves kind open (an
// extension may register any name), so this check is where a typo in kind
// is caught, and the message has to list the kinds the binary knows or the
// author cannot tell a typo from a missing extension.
func TestValidateShapeRejectsUnknownKind(t *testing.T) {
	_, err := ValidateShapeWith(&SchemaConfig{Name: "x", Kind: ir.SchemaKind("DBB")}, coreKinds)
	if err == nil {
		t.Fatal("ValidateShapeWith accepted a kind the set does not know")
	}
	want := `schema config for x has unknown kind "DBB" (registered kinds: API, DB, General)`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	// The set is the caller's: a kind an extension registers is accepted
	// and shows up in the list for the next typo.
	withPlatform := append(kindList{}, coreKinds...)
	withPlatform = append(withPlatform, "Platform")
	if _, err := ValidateShapeWith(&SchemaConfig{Name: "x", Kind: ir.SchemaKind("Platform")}, withPlatform); err != nil {
		t.Errorf("ValidateShapeWith rejected a kind the set knows: %v", err)
	}
	_, err = ValidateShapeWith(&SchemaConfig{Name: "x", Kind: ir.SchemaKind("Platfrom")}, withPlatform)
	if err == nil || !strings.Contains(err.Error(), "registered kinds: API, DB, General, Platform") {
		t.Errorf("error %q does not list the extension kind", err)
	}
}

// TestReadFileNamesTheRegisteredKindsOnATypo is the data-form path end to
// end: the JSON Schema accepts any string for kind, so the typo reaches
// ValidateShapeWith and the diagnostic must name the kinds.
func TestReadFileNamesTheRegisteredKindsOnATypo(t *testing.T) {
	dir := writeConfig(t, "schema.config.json", `{"name": "web-db", "kind": "DBB", "outputs": {}}`)
	_, err := ReadFile(dir, coreKinds)
	if err == nil {
		t.Fatal("ReadFile accepted kind DBB")
	}
	if !strings.Contains(err.Error(), `unknown kind "DBB"`) || !strings.Contains(err.Error(), "registered kinds: API, DB, General") {
		t.Errorf("error %q does not name the typo and the registered kinds", err.Error())
	}
}

func TestReadFileMissing(t *testing.T) {
	if _, err := ReadFile(t.TempDir(), coreKinds); err == nil {
		t.Fatal("ReadFile succeeded with no config present")
	}
}

// TestEmbeddedDefinitionIsCurrent regenerates the JSON Schema from
// @superschematic/schema-config and diffs it against the embedded artifact, so the
// checked-in copy cannot drift from the TypeScript contract. Skipped when
// bun or the package's node_modules are unavailable.
//
// ts-json-schema-generator writes no trailing newline and the repo's
// end-of-file-fixer hook adds one, so the comparison ignores a single
// trailing newline on either side.
func TestEmbeddedDefinitionIsCurrent(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not installed")
	}
	pkgDir, err := filepath.Abs(filepath.Join("..", "..", "..", "packages", "schema-config"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "node_modules")); err != nil {
		t.Skip("packages/schema-config dependencies not installed (run bun install)")
	}

	out := filepath.Join(t.TempDir(), "schema-config.schema.json")
	cmd := exec.Command(bun, "run", "ts-json-schema-generator",
		"--path", "src/index.ts",
		"--type", "SchemaConfigDocument",
		"--id", "superschematic://schema-config.schema.json",
		"--out", out)
	cmd.Dir = pkgDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("regenerating schema: %v\n%s", err, output)
	}

	regenerated, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(string(regenerated), "\n") != strings.TrimSuffix(string(definitionBytes), "\n") {
		t.Error("embedded schema-config.schema.json is stale; run: cd packages/schema-config && bun run gen-json-schema")
	}
}
