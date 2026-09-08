package naming

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("missing file must yield Default(), got %+v", got)
	}
	if got.GoTypesModule("web-db") != "github.com/parable-platform/platform-schemas/types/go/web-db" {
		t.Errorf("GoTypesModule = %q", got.GoTypesModule("web-db"))
	}
}

func TestLoadFileOverridesAndKeepsUnsetDefaults(t *testing.T) {
	root := t.TempDir()
	content := "go_module_root = \"example.com/acme/schemas\"\nnpm_scope = \"@acme\"\nrust_crate_prefix = \"acme-\"\n"
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.GoModuleRoot != "example.com/acme/schemas" {
		t.Errorf("GoModuleRoot = %q", got.GoModuleRoot)
	}
	if got.NpmTypesPackage("orders") != "@acme/orders-types" {
		t.Errorf("NpmTypesPackage = %q", got.NpmTypesPackage("orders"))
	}
	if got.RustSDKCrate("orders") != "acme-orders-sdk" {
		t.Errorf("RustSDKCrate = %q", got.RustSDKCrate("orders"))
	}
	if got.ScalarGoModule != Default().ScalarGoModule {
		t.Errorf("unset key must keep default, got %q", got.ScalarGoModule)
	}
	if got.PythonSDKModule("orders") != "parable_orders_sdk" {
		t.Errorf("PythonSDKModule = %q", got.PythonSDKModule("orders"))
	}
}

func TestLoadMalformedFileErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("go_module_root = \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	if err == nil {
		t.Fatal("malformed TOML must error")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Errorf("error must name the file, got %v", err)
	}
}

func TestLoadUnknownKeyErrors(t *testing.T) {
	_, err := Parse([]byte("go_module_rot = \"x\"\n"), "test.toml")
	if err == nil {
		t.Fatal("unknown key must error")
	}
	if !strings.Contains(err.Error(), "go_module_rot") {
		t.Errorf("error must name the key, got %v", err)
	}
}

func TestParseKeepsExtensionTables(t *testing.T) {
	src := "npm_scope = \"@acme\"\n\n[extension.acme]\nauth_provider = \"acme-sso\"\nretries = 3\n\n[extension.acme.lake]\nbucket = \"gs://acme\"\n\n[extension.other]\nflag = true\n"
	got, err := Parse([]byte(src), "test.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.NpmScope != "@acme" {
		t.Errorf("NpmScope = %q", got.NpmScope)
	}
	acme, ok := got.ExtensionConfig("acme")
	if !ok {
		t.Fatal("ExtensionConfig(acme) must be present")
	}
	if acme["auth_provider"] != "acme-sso" {
		t.Errorf("auth_provider = %v", acme["auth_provider"])
	}
	if acme["retries"] != int64(3) {
		t.Errorf("retries = %v (%T)", acme["retries"], acme["retries"])
	}
	lake, ok := acme["lake"].(map[string]any)
	if !ok || lake["bucket"] != "gs://acme" {
		t.Errorf("nested table lake = %v", acme["lake"])
	}
	if _, ok := got.ExtensionConfig("other"); !ok {
		t.Error("ExtensionConfig(other) must be present")
	}
	if _, ok := got.ExtensionConfig("missing"); ok {
		t.Error("ExtensionConfig(missing) must report absent")
	}
	if _, ok := Default().ExtensionConfig("acme"); ok {
		t.Error("Default() carries no extension tables")
	}
}

func TestParseExtensionTablesSurviveOrDefaultAndSetActive(t *testing.T) {
	t.Cleanup(func() { SetActive(Default()) })
	got, err := Parse([]byte("[extension.acme]\nk = \"v\"\n"), "test.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.GoModuleRoot != Default().GoModuleRoot {
		t.Errorf("naming keys must keep defaults, got %q", got.GoModuleRoot)
	}
	SetActive(got)
	cfg, ok := Active().ExtensionConfig("acme")
	if !ok || cfg["k"] != "v" {
		t.Errorf("Active().ExtensionConfig(acme) = %v, %v", cfg, ok)
	}
}

func TestParseRejectsUnknownTopLevelKeyNextToExtensionTable(t *testing.T) {
	_, err := Parse([]byte("go_module_rot = \"x\"\n\n[extension.acme]\nk = \"v\"\n"), "test.toml")
	if err == nil {
		t.Fatal("unknown top-level key must error even when extension tables are present")
	}
	if !strings.Contains(err.Error(), "go_module_rot") {
		t.Errorf("error must name the key, got %v", err)
	}
	if strings.Contains(err.Error(), "extension.acme") {
		t.Errorf("error must not blame the extension table, got %v", err)
	}
}

func TestParseReadsPathsTable(t *testing.T) {
	n, err := Parse([]byte("[paths]\nscalar_lib = \"utils/parable-scalars\"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if n.Paths.ScalarLib != "utils/parable-scalars" {
		t.Fatalf("Paths.ScalarLib = %q", n.Paths.ScalarLib)
	}
	want := filepath.Join("/repo", "utils", "parable-scalars")
	if got := n.ScalarLibPath("/repo"); got != want {
		t.Fatalf("ScalarLibPath = %q, want %q", got, want)
	}
	if got := Default().ScalarLibPath("/repo"); got != "" {
		t.Fatalf("ScalarLibPath with no [paths] = %q, want empty", got)
	}
	if _, err := Parse([]byte("[paths]\nbogus = \"x\"\n"), "test"); err == nil {
		t.Fatal("Parse accepted an unknown [paths] key")
	}
}

func TestParseReadsCacheTable(t *testing.T) {
	n, err := Parse([]byte(`
[cache]
root = "~/psgen-cache"
inputs = ["utils/parable-scalars/permissions.yml", "docs/extra.yml"]
`), "superschematic.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if n.Cache.Root != "~/psgen-cache" {
		t.Fatalf("Cache.Root = %q", n.Cache.Root)
	}
	want := []string{"utils/parable-scalars/permissions.yml", "docs/extra.yml"}
	if !reflect.DeepEqual(n.Cache.Inputs, want) {
		t.Fatalf("Cache.Inputs = %v, want %v", n.Cache.Inputs, want)
	}
	if n.NpmScope != Default().NpmScope {
		t.Fatalf("[cache] changed NpmScope to %q", n.NpmScope)
	}

	_, err = Parse([]byte("[cache]\nroots = \"x\"\n"), "superschematic.toml")
	if err == nil || !strings.Contains(err.Error(), "cache.roots") {
		t.Fatalf("unknown [cache] key: err = %v, want cache.roots rejected", err)
	}
}

func TestParseRejectsUnknownTopLevelTable(t *testing.T) {
	_, err := Parse([]byte("[extensions.acme]\nk = \"v\"\n"), "test.toml")
	if err == nil {
		t.Fatal("a top-level table other than [extension] must error")
	}
	if !strings.Contains(err.Error(), "extensions.acme.k") {
		t.Errorf("error must name the offending key path, got %v", err)
	}
}

func TestParseDoesNotValidateInsideExtensionTables(t *testing.T) {
	got, err := Parse([]byte("[extension.acme]\nauth_provder = \"typo\"\n"), "test.toml")
	if err != nil {
		t.Fatalf("keys inside [extension.x] are the extension's to validate, got %v", err)
	}
	cfg, ok := got.ExtensionConfig("acme")
	if !ok || cfg["auth_provder"] != "typo" {
		t.Errorf("typo key must be kept verbatim, got %v", cfg)
	}
}

func TestOrDefaultFillsEmptyFields(t *testing.T) {
	got := Naming{NpmScope: "@acme"}.OrDefault()
	if got.NpmScope != "@acme" {
		t.Errorf("set field must survive, got %q", got.NpmScope)
	}
	if got.GoModuleRoot != Default().GoModuleRoot {
		t.Errorf("empty field must be filled, got %q", got.GoModuleRoot)
	}
	if got.ScalarRustCrateIdent() != "parable_scalars_core" {
		t.Errorf("ScalarRustCrateIdent = %q", got.ScalarRustCrateIdent())
	}
}

func TestNpmTypesPackageKeepsSingleSuffix(t *testing.T) {
	n := Default()
	if n.NpmTypesPackage("connectors-web-types") != "@parable-platform/connectors-web-types" {
		t.Errorf("got %q", n.NpmTypesPackage("connectors-web-types"))
	}
	if n.NpmTypesPackage("web-db") != "@parable-platform/web-db-types" {
		t.Errorf("got %q", n.NpmTypesPackage("web-db"))
	}
}

func TestSetActiveFillsDefaults(t *testing.T) {
	t.Cleanup(func() { SetActive(Default()) })
	SetActive(Naming{GoModuleRoot: "example.com/acme/schemas"})
	got := Active()
	if got.GoModuleRoot != "example.com/acme/schemas" {
		t.Errorf("GoModuleRoot = %q", got.GoModuleRoot)
	}
	if got.NpmScope != Default().NpmScope {
		t.Errorf("NpmScope must fall back to default, got %q", got.NpmScope)
	}
}

func TestDiscoverWalksUpFromFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(`npm_scope = "@acme"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "services", "svc", "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "x.schema.ts")
	if err := os.WriteFile(file, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(file)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got.NpmScope != "@acme" {
		t.Errorf("NpmScope = %q, want @acme", got.NpmScope)
	}
	if got.GoModuleRoot != Default().GoModuleRoot {
		t.Errorf("GoModuleRoot = %q, want default", got.GoModuleRoot)
	}
}

func TestDiscoverWithoutFileReturnsDefaults(t *testing.T) {
	got, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("Discover without file = %+v, want defaults", got)
	}
}

func TestAuthoringPackagesDefaultToTodaysSetAndIncludeTheScalarPackage(t *testing.T) {
	d := Default()
	want := []string{
		"@psgen/api", "@psgen/db", "@psgen/deploy",
		"@psgen/scalar-lib", "@psgen/schema", "@psgen/schema-config",
	}
	if !reflect.DeepEqual(d.AuthoringPackages, want) {
		t.Fatalf("Default().AuthoringPackages = %v, want %v", d.AuthoringPackages, want)
	}
	if d.AuthoringScope() != "@psgen" {
		t.Errorf("AuthoringScope() = %q, want @psgen", d.AuthoringScope())
	}

	fork := Naming{ScalarNpmPackage: "@acme/scalars"}.OrDefault()
	if got := fork.AuthoringPackages[len(fork.AuthoringPackages)-1]; got != "@acme/scalars" {
		t.Errorf("renamed scalar package not appended: %v", fork.AuthoringPackages)
	}
	if fork.AuthoringScope() != "" {
		t.Errorf("mixed scopes must yield no common scope, got %q", fork.AuthoringScope())
	}

	explicit, err := Parse([]byte(`authoring_packages = ["@acme/schematic"]`), "test.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(explicit.AuthoringPackages, []string{"@acme/schematic", "@psgen/scalar-lib"}) {
		t.Errorf("explicit list = %v", explicit.AuthoringPackages)
	}
}
