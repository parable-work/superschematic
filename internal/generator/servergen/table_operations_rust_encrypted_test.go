package servergen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRustTableOperationsServerOpensItsFieldKeys: the Rust server of an
// API whose table operations are over a table with an Encrypted<T> column
// (D85, D116) reads the keyring of the database's key secret from the
// API's config and hands it to Implementations::new, as the Go and the
// TypeScript entrypoints hand theirs to the ORM they open. The fixture is
// fixture-tracker-api over fixture-tracker-db, on a SQLite file, with the
// marks set on the loaded schemas: a comment's body encrypted, the view's
// body its @decrypted member, the database's config naming the key secret
// and the API's @envVars class holding it, since no fixture declares the
// three. Its main.rs is laid out as rustfmt lays it out, and the server
// builds in the output root's workspace and passes clippy.
func TestRustTableOperationsServerOpensItsFieldKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo clippy in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	const keyType, keyField = "TrackerKeys", "TRACKER_FIELD_KEYS"
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadRustTablesFixture(t, "sqlite", "postgres")
	field := func(schema *ir.Schema, typeName, name string) *ir.FieldDef {
		t.Helper()
		if td := schema.Types[typeName]; td != nil {
			for _, fd := range td.Fields {
				if fd != nil && fd.Name == name {
					return fd
				}
			}
		}
		t.Fatalf("%s has no %s.%s", schema.Name, typeName, name)
		return nil
	}
	body := field(f.schemas[rustTablesDB], "Comment", "body")
	body.EncryptedAtRest, body.Secret = true, true
	sql, _ := f.configs[rustTablesDB].Outputs["sql"].(map[string]any)
	sql["encryption"] = map[string]any{"key": keyType + "." + keyField}
	api := f.schemas[rustTablesServer]
	field(api, "CommentView", "body").Decrypted = true
	api.Types[keyType] = &ir.TypeDef{
		Name:    keyType,
		Role:    ir.RoleEmbeddedStruct,
		EnvVars: true,
		Fields: []*ir.FieldDef{{
			Name:     keyField,
			TypeRef:  ir.TypeRef{Name: "string"},
			Required: true,
			Secret:   true,
		}},
	}
	f.build(t, repoRoot, paths, rustTablesOrder...)

	out := filepath.Join(repoRoot, "schemas", "dist")
	source := filepath.Join(servergen.ServerDir(out, rustTablesStack, rustTablesServer), filepath.FromSlash(servergen.RustMainFile))
	main, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if want := "::tables::field_keys(&"; !strings.Contains(string(main), want) {
		t.Errorf("main.rs reads no keyring from the API's config (%s):\n%s", want, main)
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
			t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
		}
	}
	cmd := exec.Command(cargo, "clippy", "--workspace", "--quiet", "--", "-D", "warnings")
	cmd.Dir = out
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+rustTargetDir(t))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo clippy in %s: %v\n%s", out, err, output)
	}
}
