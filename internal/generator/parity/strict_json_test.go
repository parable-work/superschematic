package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestStrictJSONGeneratedDecoders runs the TypeScript, Python and Rust
// decoders generated for a @strictJSON contract over the same invalid
// payloads: an undeclared key at the root and in a strict nested type, a
// mis-cased key, a null root, an empty object, an absent or null required
// field, and a null required field that has a default. Every one must be
// rejected, so an unknown field cannot disappear on a client round trip
// before the server sees it. The unmarked type keeps ignoring unknown keys.
// The Go decoder is covered by typegen's TestGeneratedStrictJSONRejectsUnknownNestedFields.
func TestStrictJSONGeneratedDecoders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-decoder check in -short mode")
	}
	schema := ir.NewSchema("strict-contract", ir.SchemaKindGeneral)
	for _, encoded := range []string{
		`{"name":"Policy","role":"EmbeddedStruct","strictJSON":true,"fields":[{"name":"grant","typeRef":{"name":"Grant"},"required":true},{"name":"enabled","typeRef":{"name":"boolean"},"required":true,"default":"false"},{"name":"optionalGrant","typeRef":{"name":"Grant"}}]}`,
		`{"name":"Grant","role":"EmbeddedStruct","strictJSON":true,"fields":[{"name":"members","typeRef":{"name":"string","isArray":true},"required":true}]}`,
		`{"name":"Ordinary","role":"EmbeddedStruct","fields":[{"name":"name","typeRef":{"name":"string"},"required":true}]}`,
	} {
		var model ir.TypeDef
		if err := json.Unmarshal([]byte(encoded), &model); err != nil {
			t.Fatal(err)
		}
		schema.Types[model.Name] = &model
	}
	const vectors = `[
  {"grant":{"members":[]},"private":true},
  {"grant":{"members":[],"onlyOwner":true}},
  {"Grant":{"members":[]}},
  null, {}, {"grant":{}}, {"grant":null}, {"grant":{"members":null}}, {"grant":{"members":[]},"enabled":null}
]`
	write := func(t *testing.T, dir, name, contents string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, dir, program string, args ...string) {
		t.Helper()
		cmd := exec.Command(program, args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", program, strings.Join(args, " "), err, output)
		}
	}

	t.Run("typescript", func(t *testing.T) {
		bunPath, err := exec.LookPath("bun")
		if err != nil {
			testpaths.RequireOrSkipTS(t, "bun not available for the TypeScript strict JSON check")
		}
		paths := testpaths.Local(t)
		out, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "strict-contract"})
		if err != nil {
			t.Fatal(err)
		}
		// macOS exposes the temp dir through /var while Bun resolves it
		// through /private/var; resolve it so the relative file: spec holds.
		tempRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(tempRoot, "strict-contract")
		if err := tsgen.SetScalarLibSpec(out, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := tsgen.WriteTypes(out, dir); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "strict.test.ts", `import assert from 'node:assert/strict';
import { parsePolicyJson, parsePolicyYaml, parsePolicyJsonNonStrict, parsePolicyFromJSON, validatePolicyRequired } from './validators/types/policy';
import { parseOrdinaryJsonNonStrict } from './validators/types/ordinary';
const valid = { grant: { members: [] } };
assert.deepEqual(parsePolicyJson({ ...valid, optionalGrant: null }), { ...valid, enabled: false, optionalGrant: null });
for (const bad of `+vectors+`) {
  for (const parse of [parsePolicyJson, parsePolicyYaml, parsePolicyJsonNonStrict, parsePolicyFromJSON]) {
    assert.throws(() => parse(bad), 'accepted invalid policy: ' + JSON.stringify(bad));
  }
  assert.equal(validatePolicyRequired(bad as never)[0], false);
}
assert.equal(parseOrdinaryJsonNonStrict({ name: 'ordinary', future: true }).name, 'ordinary');
`)
		install := exec.Command(bunPath, "install")
		install.Dir = dir
		if output, err := install.CombinedOutput(); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, output))
		}
		run(t, dir, bunPath, "x", "tsc", "--noEmit")
		run(t, dir, bunPath, "run", "strict.test.ts")
	})

	t.Run("python", func(t *testing.T) {
		pythonPath, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not available; skipping Python strict JSON check")
		}
		if err := exec.Command(pythonPath, "-c", "import pydantic").Run(); err != nil {
			t.Skip("pydantic not available; skipping Python strict JSON check")
		}
		out, err := pygen.Generate(schema, pygen.Options{SchemaName: "strict-contract"})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := pygen.WriteTypes(out, dir); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "strict_test.py", fmt.Sprintf(`import json
from %s import Policy, Ordinary
from pydantic import ValidationError
Policy.model_validate_json('{"grant":{"members":[]},"optionalGrant":null}')
for bad in json.loads(%q):
    try:
        Policy.model_validate_json(json.dumps(bad))
    except ValidationError:
        pass
    else:
        raise AssertionError('accepted invalid policy: ' + json.dumps(bad))
assert Ordinary.model_validate({'name': 'ordinary', 'future': True}).name == 'ordinary'
`, out.PythonModuleName, vectors))
		run(t, dir, pythonPath, "strict_test.py")
	})

	t.Run("rust", func(t *testing.T) {
		cargoPath, err := exec.LookPath("cargo")
		if err != nil {
			t.Skip("cargo not available; skipping Rust strict JSON check")
		}
		out, err := rustgen.Generate(schema, rustgen.Options{SchemaName: "strict-contract"})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := rustgen.WriteTypes(out, dir); err != nil {
			t.Fatal(err)
		}
		cargo, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, "Cargo.toml", string(cargo)+"\n[dev-dependencies]\nserde_json = \"1.0\"\n")
		write(t, dir, "tests/strict.rs", fmt.Sprintf(`use %s::{Ordinary, Policy};

#[test]
fn rejects_unknown_and_missing_fields() {
    assert!(serde_json::from_str::<Policy>(r#"{"grant":{"members":[]}}"#).is_ok());
    let vectors: Vec<serde_json::Value> = serde_json::from_str(r#"%s"#).unwrap();
    for bad in vectors {
        assert!(serde_json::from_value::<Policy>(bad.clone()).is_err(), "accepted: {bad}");
    }
    assert!(serde_json::from_str::<Ordinary>(r#"{"name":"ordinary","future":true}"#).is_ok());
}
`, strings.ReplaceAll(out.CrateName, "-", "_"), vectors))
		cmd := exec.Command(cargoPath, "test", "--quiet", "--test", "strict")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(dir, "target"))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo test: %v\n%s", err, output)
		}
	})
}
