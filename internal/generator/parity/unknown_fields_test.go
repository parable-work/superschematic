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
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// unknownFieldsSchema is the contract TestAllowUnknownFieldsGeneratedDecoders
// runs: Hook and Detail are @allowUnknownFields, Ordinary is not.
func unknownFieldsSchema(t *testing.T) *ir.Schema {
	t.Helper()
	schema := ir.NewSchema("lenient-contract", ir.SchemaKindGeneral)
	for _, encoded := range []string{
		`{"name":"Hook","role":"EmbeddedStruct","allowUnknownFields":true,"fields":[{"name":"item_id","typeRef":{"name":"string"},"required":true},{"name":"detail","typeRef":{"name":"Detail"}},{"name":"ordinary","typeRef":{"name":"Ordinary"}}]}`,
		`{"name":"Detail","role":"EmbeddedStruct","allowUnknownFields":true,"fields":[{"name":"code","typeRef":{"name":"string"},"required":true}]}`,
		`{"name":"Ordinary","role":"EmbeddedStruct","fields":[{"name":"name","typeRef":{"name":"string"},"required":true}]}`,
	} {
		var model ir.TypeDef
		if err := json.Unmarshal([]byte(encoded), &model); err != nil {
			t.Fatal(err)
		}
		schema.Types[model.Name] = &model
	}
	return schema
}

// The payloads every language's decoders of Hook run (D87). accepted holds
// an undeclared key at the root and in Detail, a key that differs from a
// declared one in case alone at both levels, and one in Ordinary, whose
// decoders ignore it as before: each decoder reads item_id "a" and
// detail.code "X", and what it decodes holds no undeclared key of Hook or
// Detail. refused breaks a declared key's type at the root and in Detail.
const (
	acceptedHook = `{"item_id":"a","environment":"sandbox","Item_Id":"b","detail":{"code":"X","Code":"Y","message":"m"},"ordinary":{"name":"n","future":true}}`
	refusedHooks = `[{"item_id":7,"environment":"sandbox"},{"item_id":"a","detail":{"code":1,"message":"m"}},{"item_id":"a","detail":"X"},[]]`
)

// TestAllowUnknownFieldsGeneratedDecoders runs the Go, TypeScript, Python
// and Rust decoders generated for an @allowUnknownFields contract over the
// same payloads (D87). Each strict decoder accepts the keys the type does
// not declare and drops them, a key that differs in case among them, and
// still refuses a declared key of the wrong type. The servers decode an
// input through these decoders, which their own tests run
// (apigen's TestWebhookRoutesDropUndeclaredKeys, tsrestgen's
// TestGeneratedWebhooksRouter, rustrestgen's
// TestWebhooksAPICrateBuildsAndRoutes).
func TestAllowUnknownFieldsGeneratedDecoders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-decoder check in -short mode")
	}
	schema := unknownFieldsSchema(t)
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

	t.Run("go", func(t *testing.T) {
		out, err := typegen.Generate(schema, typegen.Options{SchemaName: "lenient-contract", ModulePath: "example.com/schemas/types/go/lenient-contract"})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "lenient-contract")
		if err := typegen.SetReplacePaths(out, testpaths.Local(t), dir); err != nil {
			t.Fatal(err)
		}
		if err := typegen.WriteTypes(out, dir); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "lenient_test.go", fmt.Sprintf(`package %s

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeHook(t *testing.T) {
	unmarshal := func(data []byte) (*Hook, error) {
		var hook Hook
		err := json.Unmarshal(data, &hook)
		return &hook, err
	}
	for name, decode := range map[string]func([]byte) (*Hook, error){"FromJSON": HookFromJSON, "FromJSONNonStrict": HookFromJSONNonStrict, "Unmarshal": unmarshal} {
		hook, err := decode([]byte(%q))
		if err != nil {
			t.Fatalf("%%s refused the accepted payload: %%v", name, err)
		}
		if hook.ItemId != "a" || hook.Detail == nil || hook.Detail.Code != "X" {
			t.Fatalf("%%s decoded %%+v", name, hook)
		}
		encoded, _ := json.Marshal(hook)
		for _, key := range []string{"environment", "Item_Id", "Code", "message"} {
			if strings.Contains(string(encoded), %q+key+%q) {
				t.Fatalf("%%s kept %%s: %%s", name, key, encoded)
			}
		}
		var refused []json.RawMessage
		if err := json.Unmarshal([]byte(%q), &refused); err != nil {
			t.Fatal(err)
		}
		for _, payload := range refused {
			hook, err := decode(payload)
			if err == nil && hook != nil && !hook.Validate().HasErrors() {
				t.Errorf("%%s accepted %%s", name, payload)
			}
		}
	}
}
`, out.PackageName, acceptedHook, `"`, `"`, refusedHooks))
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = dir
		if output, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, output)
		}
		run(t, dir, "go", "test", "-count=1", "-run", "^TestDecodeHook$", ".")
	})

	t.Run("typescript", func(t *testing.T) {
		bunPath, err := exec.LookPath("bun")
		if err != nil {
			testpaths.RequireOrSkipTS(t, "bun not available for the TypeScript unknown-fields check")
		}
		paths := testpaths.Local(t)
		out, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "lenient-contract"})
		if err != nil {
			t.Fatal(err)
		}
		// macOS exposes the temp dir through /var while Bun resolves it
		// through /private/var; resolve it so the relative file: spec holds.
		tempRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(tempRoot, "lenient-contract")
		if err := tsgen.SetScalarLibSpec(out, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := tsgen.WriteTypes(out, dir); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "lenient.test.ts", `import assert from 'node:assert/strict';
import { parseHookFromJSON, parseHookJson, parseHookJsonNonStrict, parseHookYaml } from './validators/types/hook';
const accepted = `+acceptedHook+`;
const copy = () => JSON.parse(JSON.stringify(accepted)) as unknown;
// A parser drops Hook's own undeclared keys; parseHookFromJSON, which reads
// each nested value with its type's parser as a router does after it,
// drops Detail's too.
for (const parse of [parseHookJson, parseHookYaml, parseHookJsonNonStrict, parseHookFromJSON]) {
  const hook = parse(copy());
  assert.equal(hook.item_id, 'a');
  for (const key of ['environment', 'Item_Id']) assert.ok(!(key in hook), parse.name + ' kept ' + key);
  assert.deepEqual(parseHookFromJSON(hook).detail, { code: 'X' });
}
assert.deepEqual(parseHookFromJSON(parseHookJson(JSON.stringify(accepted))).detail, { code: 'X' });
assert.equal(accepted.environment, 'sandbox', 'a parser does not change its input');
for (const bad of `+refusedHooks+`) {
  for (const parse of [parseHookJson, parseHookYaml, parseHookJsonNonStrict]) {
    assert.throws(() => parse(bad), 'accepted invalid hook: ' + JSON.stringify(bad));
  }
}
`)
		install := exec.Command(bunPath, "install")
		install.Dir = dir
		if output, err := install.CombinedOutput(); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, output))
		}
		run(t, dir, bunPath, "x", "tsc", "--noEmit")
		run(t, dir, bunPath, "run", "lenient.test.ts")
	})

	t.Run("python", func(t *testing.T) {
		pythonPath, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not available; skipping Python unknown-fields check")
		}
		if err := exec.Command(pythonPath, "-c", "import pydantic").Run(); err != nil {
			t.Skip("pydantic not available; skipping Python unknown-fields check")
		}
		out, err := pygen.Generate(schema, pygen.Options{SchemaName: "lenient-contract"})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := pygen.WriteTypes(out, dir); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "lenient_test.py", fmt.Sprintf(`import json
from %s import Hook
from pydantic import ValidationError
accepted = %q
for hook in (Hook.model_validate_json(accepted), Hook.from_json(accepted), Hook.from_dict(json.loads(accepted))):
    assert hook.item_id == 'a' and hook.detail.code == 'X', hook
    dumped = hook.model_dump()
    assert 'environment' not in dumped and 'Item_Id' not in dumped, dumped
    assert dumped['detail'] == {'code': 'X'}, dumped
for bad in json.loads(%q):
    try:
        Hook.model_validate_json(json.dumps(bad))
    except ValidationError:
        pass
    else:
        raise AssertionError('accepted invalid hook: ' + json.dumps(bad))
`, out.PythonModuleName, acceptedHook, refusedHooks))
		run(t, dir, pythonPath, "lenient_test.py")
	})

	t.Run("rust", func(t *testing.T) {
		cargoPath, err := exec.LookPath("cargo")
		if err != nil {
			t.Skip("cargo not available; skipping Rust unknown-fields check")
		}
		out, err := rustgen.Generate(schema, rustgen.Options{SchemaName: "lenient-contract"})
		if err != nil {
			t.Fatal(err)
		}
		dir := testpaths.TempDir(t)
		if err := rustgen.SetLocalPaths(out, testpaths.Local(t), dir); err != nil {
			t.Fatal(err)
		}
		if err := rustgen.WriteTypes(out, dir); err != nil {
			t.Fatal(err)
		}
		cargo, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, "Cargo.toml", string(cargo)+"\n[dev-dependencies]\nserde_json = \"1.0\"\n")
		crate := strings.ReplaceAll(out.CrateName, "-", "_")
		write(t, dir, "tests/lenient.rs", fmt.Sprintf(`use %s::validators::{parse_hook, prepare_hook};
use %s::Hook;
use superschematic_schema_runtime::UnknownFields;

#[test]
fn drops_the_keys_the_type_does_not_declare() {
    let accepted: serde_json::Value = serde_json::from_str(r#"%s"#).unwrap();
    for policy in [UnknownFields::Refuse, UnknownFields::Allow] {
        let hook = parse_hook(accepted.clone(), policy).expect("refused the accepted payload");
        assert_eq!(hook.item_id, "a");
        let encoded = serde_json::to_value(&hook).unwrap();
        assert_eq!(encoded["detail"], serde_json::json!({"code": "X"}));
        assert!(encoded.get("environment").is_none() && encoded.get("Item_Id").is_none(), "{encoded}");
        let prepared = prepare_hook(accepted.clone(), policy).unwrap();
        assert!(prepared.get("environment").is_none() && prepared.get("Item_Id").is_none(), "{prepared}");
    }
    let decoded: Hook = serde_json::from_value(accepted).unwrap();
    assert_eq!(decoded.item_id, "a");
    let refused: Vec<serde_json::Value> = serde_json::from_str(r#"%s"#).unwrap();
    for bad in refused {
        assert!(parse_hook(bad.clone(), UnknownFields::Refuse).is_err(), "accepted: {bad}");
    }
}
`, crate, crate, acceptedHook, refusedHooks))
		cmd := exec.Command(cargoPath, "test", "--quiet", "--test", "lenient")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(dir, "target"))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo test: %v\n%s", err, output)
		}
	})
}
