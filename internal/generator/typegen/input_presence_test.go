package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedInputFieldPresenceRoundTrips generates an input type whose
// optional fields are InputField wrappers, runs a test inside the generated
// module, and checks that encoding reproduces the decoded payload exactly:
// an absent key stays absent, and a present null, false, zero, "" or empty
// collection stays present. It holds for the value and the pointer, and for
// the type nested in a struct, a slice and a map.
func TestGeneratedInputFieldPresenceRoundTrips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatal(err)
	}
	def := "30"
	schema.Types["PresenceInput"] = &ir.TypeDef{Name: "PresenceInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "text", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "flag", TypeRef: ir.TypeRef{Name: "boolean"}},
		{Name: "count", TypeRef: ir.TypeRef{Name: "number"}},
		{Name: "timeout", TypeRef: ir.TypeRef{Name: "number"}, Default: &def},
		{Name: "list", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
		{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
	}}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-general",
		ModulePath: "example.com/schemas/types/go/fixture-general",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "fixture-general")
	if err := SetReplacePaths(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"reflect"
	"testing"
)

func jsonValue(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestPresence(t *testing.T) {
	for _, payload := range []string{
		"{}",
		"{\"text\":null,\"flag\":null,\"count\":null,\"timeout\":null,\"list\":null,\"labels\":null}",
		"{\"text\":\"\",\"flag\":false,\"count\":0,\"timeout\":0,\"list\":[],\"labels\":{}}",
		"{\"text\":\"rename only\"}",
		"{\"timeout\":30}",
	} {
		var value PresenceInput
		var expected any
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(payload), &expected); err != nil {
			t.Fatal(err)
		}
		for _, representation := range []any{value, &value} {
			if actual := jsonValue(t, representation); !reflect.DeepEqual(actual, expected) {
				t.Fatalf("round trip %T of %s: %#v", representation, payload, actual)
			}
		}
		if actual := jsonValue(t, struct{ Nested PresenceInput }{value}); !reflect.DeepEqual(actual, map[string]any{"Nested": expected}) {
			t.Fatalf("struct lost presence: %#v", actual)
		}
		if actual := jsonValue(t, []PresenceInput{value}); !reflect.DeepEqual(actual, []any{expected}) {
			t.Fatalf("slice lost presence: %#v", actual)
		}
		if actual := jsonValue(t, map[string]PresenceInput{"nested": value}); !reflect.DeepEqual(actual, map[string]any{"nested": expected}) {
			t.Fatalf("map lost presence: %#v", actual)
		}
	}
	// An unset wrapper stays absent even when its unused value is not zero.
	if actual := jsonValue(t, PresenceInput{Text: InputField[string]{Value: "unused"}}); !reflect.DeepEqual(actual, map[string]any{}) {
		t.Fatalf("unset stale value became present: %#v", actual)
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "input_presence_test.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	command := exec.Command("go", "test", "-count=1", "-run", "^TestPresence$", ".")
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated input presence failed: %v\n%s", err, out)
	}
}
