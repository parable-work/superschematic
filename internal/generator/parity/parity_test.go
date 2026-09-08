// Package parity runs the same JSON payloads through the validators psgen
// generates for Go, TypeScript, and Python and asserts every language returns
// the same verdicts. One schema (built in a temp dir, JSON-authored so the
// real loader runs), one vector table, one expected-outcome column: a
// validator semantic that drifts in any language fails here. This is the
// generator-layer counterpart of scalar-lib's shared conformance corpus
// (utils/parable-scalars/conformance/), and the class of bug it exists for is
// PARABLE-3350: Go decode turned absent optional lists into empty ones, so
// only Go fired listMin on omitted fields. Reverting that fix makes the go
// subtest fail on every vector that omits optList.
//
// A divergence a PR cannot fix on the spot gets pinned in knownDivergences
// (the table is empty today) with the ticket that tracks it, mirroring the
// unresolved flag in the scalar corpus. Fixing the language later fails the
// stale pin, so the table shrinks in the same change; a new divergence fails
// against the expected column immediately. Grow coverage by adding fields to
// the matrix schema and rows to the vector table.
//
// Verdict comparison is about semantics, not field-name idiom: the Python
// driver maps validate_all's snake_case attribute keys back to wire names
// before reporting.
package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// verdicts is one language's normalized output: vector name -> field name ->
// sorted validator names that failed. A passing vector maps to an empty map.
type verdicts map[string]map[string][]string

const schemaConfigJSON = `{
  "name": "parity-fixture",
  "kind": "General",
  "outputs": {
    "types": {
      "typescript": { "enabled": true },
      "python": { "enabled": true }
    }
  }
}`

// The validation matrix: every combination of required/optional x scalar/list
// the generated validators gate differently, with one constraint per axis.
// The url field exists only to pull in a scalar so typegen emits scalars.go
// (the ValidationErrors alias lives there).
const parityMatrixSchemaJSON = `{
  "scalars": {
    "Network.Url": {
      "name": "Network.Url",
      "languagePrimitive": "string"
    }
  },
  "types": {
    "ParityMatrix": {
      "name": "ParityMatrix",
      "role": "EmbeddedStruct",
      "jsonField": true,
      "fields": [
        {
          "name": "url",
          "typeRef": { "name": "Network.Url" }
        },
        {
          "name": "reqStr",
          "typeRef": { "name": "string" },
          "required": true,
          "validateMaxLength": 5
        },
        {
          "name": "optStr",
          "typeRef": { "name": "string" },
          "validateMaxLength": 5
        },
        {
          "name": "reqList",
          "typeRef": { "name": "string", "isArray": true },
          "required": true,
          "validateMaxLength": 5,
          "validateListMin": 1,
          "validateListMax": 3
        },
        {
          "name": "optList",
          "typeRef": { "name": "string", "isArray": true },
          "validateMaxLength": 5,
          "validateListMin": 1,
          "validateListMax": 3
        },
        {
          "name": "optNum",
          "typeRef": { "name": "number" },
          "validateMin": 1,
          "validateMax": 10
        }
      ]
    }
  }
}`

// vectors are the shared payloads with the language-agnostic expected
// verdicts. "want" is what EVERY language should return; knownDivergences
// overrides it per language where behavior differs today.
var vectors = []struct {
	name    string
	payload string
	want    map[string][]string
}{
	{
		name:    "valid_full",
		payload: `{"reqStr": "ok", "optStr": "ok", "reqList": ["a"], "optList": ["b"], "optNum": 5.5}`,
		want:    map[string][]string{},
	},
	{
		// The PARABLE-3350 regression shape: omitted optional list must not
		// trip listMin.
		name:    "optional_fields_absent",
		payload: `{"reqStr": "ok", "reqList": ["a"]}`,
		want:    map[string][]string{},
	},
	{
		name:    "optional_fields_null",
		payload: `{"reqStr": "ok", "reqList": ["a"], "optStr": null, "optList": null, "optNum": null}`,
		want:    map[string][]string{},
	},
	{
		name:    "opt_list_explicit_empty",
		payload: `{"reqStr": "ok", "reqList": ["a"], "optList": []}`,
		want:    map[string][]string{"optList": {"listMin"}},
	},
	{
		name:    "opt_list_over_listmax",
		payload: `{"reqStr": "ok", "reqList": ["a"], "optList": ["a", "b", "c", "d"]}`,
		want:    map[string][]string{"optList": {"listMax"}},
	},
	{
		name:    "opt_list_item_too_long",
		payload: `{"reqStr": "ok", "reqList": ["a"], "optList": ["toolong"]}`,
		want:    map[string][]string{"optList[0]": {"maxLength"}},
	},
	{
		name:    "req_str_too_long",
		payload: `{"reqStr": "toolong", "reqList": ["a"]}`,
		want:    map[string][]string{"reqStr": {"maxLength"}},
	},
	{
		// All three languages report listMin (not required) for an
		// explicitly empty required list that carries a listMin constraint.
		name:    "req_list_explicit_empty",
		payload: `{"reqStr": "ok", "reqList": []}`,
		want:    map[string][]string{"reqList": {"listMin"}},
	},
	{
		name:    "opt_str_too_long",
		payload: `{"reqStr": "ok", "reqList": ["a"], "optStr": "toolong"}`,
		want:    map[string][]string{"optStr": {"maxLength"}},
	},
	{
		name:    "opt_num_below_min",
		payload: `{"reqStr": "ok", "reqList": ["a"], "optNum": 0.5}`,
		want:    map[string][]string{"optNum": {"min"}},
	},
}

// knownDivergences pins where a language's generated validator disagrees with
// the expected column today. Key: language -> vector name -> that language's
// actual verdicts. Every entry cites the ticket tracking the fix; when the
// generator is fixed the pin goes stale and this test fails, forcing the
// entry's removal in the same change.
var knownDivergences = map[string]map[string]map[string][]string{}

func stageParityService(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(schemaConfigJSON), 0o644); err != nil {
		t.Fatalf("write schema.config.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "parity-matrix.schema.json"), []byte(parityMatrixSchemaJSON), 0o644); err != nil {
		t.Fatalf("write parity-matrix.schema.json: %v", err)
	}
	return dir
}

func writeVectorsFile(t *testing.T, dir string) string {
	t.Helper()
	payloads := map[string]json.RawMessage{}
	for _, v := range vectors {
		payloads[v.name] = json.RawMessage(v.payload)
	}
	data, err := json.MarshalIndent(payloads, "", "  ")
	if err != nil {
		t.Fatalf("marshal vectors: %v", err)
	}
	path := filepath.Join(dir, "vectors.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write vectors: %v", err)
	}
	return path
}

func readResults(t *testing.T, path string) verdicts {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read driver results: %v", err)
	}
	var results verdicts
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("decode driver results: %v", err)
	}
	return results
}

// assertVerdicts compares one language's results against the expected column,
// applying that language's knownDivergences pins.
func assertVerdicts(t *testing.T, lang string, results verdicts) {
	t.Helper()
	for _, v := range vectors {
		got, ok := results[v.name]
		if !ok {
			t.Errorf("%s: vector %s missing from driver results", lang, v.name)
			continue
		}
		want := v.want
		if pinned, ok := knownDivergences[lang][v.name]; ok {
			want = pinned
		}
		if got == nil {
			got = map[string][]string{}
		}
		normalized := map[string][]string{}
		for field, validators := range got {
			sorted := append([]string(nil), validators...)
			sort.Strings(sorted)
			normalized[field] = sorted
		}
		if !reflect.DeepEqual(normalized, want) {
			t.Errorf("%s: vector %s verdicts diverge\n  payload: %s\n  want: %v\n  got:  %v",
				lang, v.name, v.payload, want, normalized)
		}
	}
	for name := range results {
		found := false
		for _, v := range vectors {
			if v.name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: driver returned verdicts for unknown vector %s", lang, name)
		}
	}
}

const goDriverTest = `package types

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

func TestValidationParityDriver(t *testing.T) {
	vectorsPath := os.Getenv("PARITY_VECTORS")
	resultsPath := os.Getenv("PARITY_RESULTS")
	if vectorsPath == "" || resultsPath == "" {
		t.Skip("PARITY_VECTORS / PARITY_RESULTS not set")
	}
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var payloads map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payloads); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	results := map[string]map[string][]string{}
	for name, payload := range payloads {
		var m ParityMatrix
		if err := json.Unmarshal(payload, &m); err != nil {
			t.Fatalf("decode vector %s: %v", name, err)
		}
		errs := m.Validate()
		fields := map[string][]string{}
		for field := range errs {
			var validators []string
			for _, fe := range errs.GetFieldErrors(field) {
				validators = append(validators, fe.Validator)
			}
			sort.Strings(validators)
			fields[field] = validators
		}
		results[name] = fields
	}
	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("marshal results: %v", err)
	}
	if err := os.WriteFile(resultsPath, out, 0o644); err != nil {
		t.Fatalf("write results: %v", err)
	}
}
`

const tsDriver = `import { readFileSync, writeFileSync } from 'node:fs';
import { validateParityMatrix } from './validators/types/paritymatrix';

const payloads = JSON.parse(readFileSync(process.env.PARITY_VECTORS as string, 'utf8'));
const results: Record<string, Record<string, string[]>> = {};
for (const [name, payload] of Object.entries(payloads)) {
  const res = validateParityMatrix(payload as never);
  const fields: Record<string, string[]> = {};
  if (res !== true) {
    for (const [field, errs] of Object.entries(res)) {
      if (Array.isArray(errs)) {
        fields[field] = errs.map((e) => e.validator).sort();
      }
    }
  }
  results[name] = fields;
}
writeFileSync(process.env.PARITY_RESULTS as string, JSON.stringify(results, null, 2));
`

// pyDriver decodes via model_fields alias mapping + model_construct so
// validate_all sees the payload without pydantic's own decode validation in
// the way (mirrors how Go and TS drive their validators directly). Error
// keys come back as python attribute names (opt_list, opt_list[0]); the
// driver maps them to wire names so the comparison is about verdicts, not
// each language's field-name idiom.
func pyDriver(outDir, moduleName string) string {
	return fmt.Sprintf(`
import importlib
import json
import os
import re
import sys

sys.path.insert(0, %q)
mod = importlib.import_module(%q)
Model = getattr(mod, "ParityMatrix")

aliases = {}
for attr, field in Model.model_fields.items():
    aliases[attr] = field.alias or attr

def to_wire(key):
    m = re.match(r"^([A-Za-z0-9_]+)(.*)$", key)
    if not m:
        return key
    return aliases.get(m.group(1), m.group(1)) + m.group(2)

with open(os.environ["PARITY_VECTORS"]) as f:
    payloads = json.load(f)

results = {}
for name, payload in payloads.items():
    data = {}
    for attr, field in Model.model_fields.items():
        key = field.alias or attr
        if key in payload:
            data[attr] = payload[key]
    m = Model.model_construct(**data)
    errs = m.validate_all()
    fields = {}
    for field_name, entries in errs.errors.items():
        fields[to_wire(field_name)] = sorted(e["validator"] for e in entries)
    results[name] = fields

with open(os.environ["PARITY_RESULTS"], "w") as f:
    json.dump(results, f)
`, outDir, moduleName)
}

func TestGeneratedValidatorParity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-runtime parity check in -short mode")
	}

	serviceDir := stageParityService(t)
	schema, err := loader.LoadService(serviceDir)
	if err != nil {
		t.Fatalf("load parity fixture: %v", err)
	}

	sharedDir := t.TempDir()
	vectorsPath := writeVectorsFile(t, sharedDir)
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	t.Run("go", func(t *testing.T) {
		paths := testpaths.Local(t)

		output, err := typegen.Generate(schema, typegen.Options{
			SchemaName: "parity-fixture",
			ModulePath: "example.com/schemas/types/go/parity-fixture",
			Clock:      fixedClock,
		})
		if err != nil {
			t.Fatalf("generate go: %v", err)
		}
		// Resolve symlinks (macOS /var -> /private/var) so the relative
		// replace path computed against the temp dir resolves at build time.
		tempRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve temp dir: %v", err)
		}
		outDir := filepath.Join(tempRoot, "parity-fixture")
		if err := typegen.SetReplacePaths(output, paths, outDir); err != nil {
			t.Fatalf("set replace paths: %v", err)
		}
		if err := typegen.WriteTypes(output, outDir); err != nil {
			t.Fatalf("write go types: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "parity_driver_test.go"), []byte(goDriverTest), 0o644); err != nil {
			t.Fatalf("write go driver: %v", err)
		}

		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = outDir
		if out, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
		}

		resultsPath := filepath.Join(sharedDir, "results-go.json")
		run := exec.Command("go", "test", "-count=1", "-run", "TestValidationParityDriver", "./...")
		run.Dir = outDir
		run.Env = append(os.Environ(), "PARITY_VECTORS="+vectorsPath, "PARITY_RESULTS="+resultsPath)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("go driver failed: %v\n%s", err, out)
		}
		assertVerdicts(t, "go", readResults(t, resultsPath))
	})

	t.Run("typescript", func(t *testing.T) {
		bunPath, err := exec.LookPath("bun")
		if err != nil {
			t.Skip("bun not available; skipping TypeScript parity check")
		}
		paths := testpaths.Local(t)

		output, err := tsgen.Generate(schema, tsgen.Options{
			SchemaName: "parity-fixture",
			Clock:      fixedClock,
		})
		if err != nil {
			t.Fatalf("generate typescript: %v", err)
		}
		tempRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve temp dir: %v", err)
		}
		outDir := filepath.Join(tempRoot, "parity-fixture")
		if err := tsgen.SetScalarLibSpec(output, paths, outDir); err != nil {
			t.Fatalf("set scalar-lib spec: %v", err)
		}
		if err := tsgen.WriteTypes(output, outDir); err != nil {
			t.Fatalf("write typescript types: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "parity_driver.ts"), []byte(tsDriver), 0o644); err != nil {
			t.Fatalf("write ts driver: %v", err)
		}

		install := exec.Command(bunPath, "install")
		install.Dir = outDir
		if out, err := install.CombinedOutput(); err != nil {
			t.Skipf("bun install failed (likely offline): %v\n%s", err, out)
		}

		resultsPath := filepath.Join(sharedDir, "results-ts.json")
		run := exec.Command(bunPath, "run", "parity_driver.ts")
		run.Dir = outDir
		run.Env = append(os.Environ(), "PARITY_VECTORS="+vectorsPath, "PARITY_RESULTS="+resultsPath)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("typescript driver failed: %v\n%s", err, out)
		}
		assertVerdicts(t, "typescript", readResults(t, resultsPath))
	})

	t.Run("python", func(t *testing.T) {
		pythonPath, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not available; skipping Python parity check")
		}
		probe := exec.Command(pythonPath, "-c", "import pydantic")
		if err := probe.Run(); err != nil {
			t.Skip("pydantic not available; skipping Python parity check")
		}

		output, err := pygen.Generate(schema, pygen.Options{
			SchemaName: "parity-fixture",
			Clock:      fixedClock,
		})
		if err != nil {
			t.Fatalf("generate python: %v", err)
		}
		outDir := filepath.Join(t.TempDir(), "parity-fixture")
		if err := pygen.WriteTypes(output, outDir); err != nil {
			t.Fatalf("write python types: %v", err)
		}

		resultsPath := filepath.Join(sharedDir, "results-py.json")
		run := exec.Command(pythonPath, "-c", pyDriver(outDir, output.PythonModuleName))
		run.Env = append(os.Environ(), "PARITY_VECTORS="+vectorsPath, "PARITY_RESULTS="+resultsPath)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("python driver failed: %v\n%s", err, out)
		}
		assertVerdicts(t, "python", readResults(t, resultsPath))
	})
}
