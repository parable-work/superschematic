package typegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// strictJSONSchema is the contract the strict-JSON tests share: Policy and
// Grant carry @strictJSON, Ordinary does not.
func strictJSONSchema(t *testing.T) *ir.Schema {
	t.Helper()
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
	return schema
}

// TestGeneratedStrictJSONRejectsUnknownNestedFields generates the Go module,
// drops a test into it and runs it: a strict type rejects an undeclared key
// at its own level and in a strict nested type, a null root, a null required
// field that has a default, and an absent required field. The unmarked type
// still ignores an unknown key. The strict type's OpenAPI schema accessor
// returns a fresh schema that forbids unknown keys.
func TestGeneratedStrictJSONRejectsUnknownNestedFields(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)

	out, err := Generate(strictJSONSchema(t), Options{SchemaName: "strict-contract", ModulePath: "example.com/schemas/types/go/strict-contract"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "strict-contract")
	if err := SetReplacePaths(out, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(out, dir); err != nil {
		t.Fatal(err)
	}
	test := "package " + out.PackageName + "\n" + `
import ("encoding/json"; "testing")
func TestDecodePolicy(t *testing.T) {
	var request struct { Policy Policy }
	if err := json.Unmarshal([]byte("{\"Policy\":{\"grant\":{\"members\":[]}}}"), &request); err != nil { t.Fatal(err) }
	if request.Policy.Enabled { t.Fatal("absent defaulted field lost its default") }
	for _, input := range []string{
		"{\"Policy\":{\"grant\":{\"members\":[]},\"private\":true}}",
		"{\"Policy\":{\"grant\":{\"members\":[],\"onlyOwner\":true}}}",
		"{\"Policy\":null}",
		"{\"Policy\":{\"grant\":{\"members\":[]},\"enabled\":null}}",
		"{\"Policy\":{}}",
		"{\"Policy\":{\"grant\":{}}}",
	} {
		if err := json.Unmarshal([]byte(input), &request); err == nil { t.Errorf("invalid policy accepted: %s", input) }
	}
	var ordinary Ordinary
	if err := json.Unmarshal([]byte("{\"name\":\"ordinary\",\"future\":true}"), &ordinary); err != nil { t.Fatalf("unmarked decoding changed: %v", err) }
	schema := PolicyOpenAPISchema()
	if schema["additionalProperties"] != false || schema["title"] != "Policy" { t.Fatalf("Policy OpenAPI schema = %v", schema) }
	schema["title"] = "changed"
	if PolicyOpenAPISchema()["title"] != "Policy" { t.Fatal("the accessor must return a fresh copy") }
}
`
	if err := os.WriteFile(filepath.Join(dir, "strict_test.go"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, output)
	}
	command := exec.Command("go", "test", "-count=1", "-run", "^TestDecodePolicy$", ".")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated decoder: %v\n%s", err, output)
	}
}
