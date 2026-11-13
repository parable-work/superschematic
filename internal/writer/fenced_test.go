package writer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
	"gopkg.in/yaml.v3"
)

// withoutScalarDefaults returns a copy of schema whose counters have no
// default: the TypeScript writer has no form for a Default on a scalar,
// which is no part of a fence.
func withoutScalarDefaults(t *testing.T, schema *ir.Schema) *ir.Schema {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var clone ir.Schema
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	for _, td := range clone.Types {
		for _, f := range td.Fields {
			if f.TypeRef.Name == "Generic.Int64" {
				f.Default = nil
			}
		}
	}
	return &clone
}

// TestWritersRoundTripFences: every writer writes a table's @fenced
// declarations and the reader reads them back (D118). The TypeScript
// writer writes each as the decorator, a fence with no covers as its field
// alone and the covered fields in the order written, a relation by its
// field's name; the data forms write the same list under fences.
func TestWritersRoundTripFences(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-fenced-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	want := normalizeIR(t, schema)
	for _, target := range []Format{FormatJSON, FormatYAML} {
		if got := normalizeIR(t, writeAndReload(t, schema, target)); got != want {
			t.Errorf("IR mismatch after TS -> %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
		}
	}
	plain := withoutScalarDefaults(t, schema)
	if got, want := normalizeIR(t, writeAndReload(t, plain, FormatTS)), normalizeIR(t, plain); got != want {
		t.Errorf("IR mismatch after TS -> ts round trip\nwant:\n%s\ngot:\n%s", want, got)
	}

	dir := t.TempDir()
	if _, err := WriteService(plain, FormatTS, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", "dispatch.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`fenced } from "@superschematic/schema";`,
		`@fenced<Parcel>({ field: "version" })`,
		`@fenced<Delivery>({ field: "planVersion", covers: ["address", "courier"] })`,
		`@fenced<Delivery>({ field: "runVersion", covers: ["attempts", "proof"] })`,
		`@fenced<Depot>({ field: "revision", covers: ["name", "deletedAt"] })`,
	} {
		if !strings.Contains(string(data), line) {
			t.Errorf("written TypeScript lacks %q:\n%s", line, data)
		}
	}
	// A table with no fence is written with none.
	if strings.Contains(string(data), "@fenced<Courier>") {
		t.Errorf("written TypeScript fences Courier:\n%s", data)
	}

	type fenced struct {
		Types map[string]struct {
			Fences []*ir.Fence `json:"fences" yaml:"fences"`
		} `json:"types" yaml:"types"`
	}
	held := func(doc fenced) bool {
		delivery, parcel := doc.Types["Delivery"].Fences, doc.Types["Parcel"].Fences
		return len(delivery) == 2 && delivery[0].Field == "planVersion" && strings.Join(delivery[0].Covers, ",") == "address,courier" &&
			delivery[1].Field == "runVersion" && strings.Join(delivery[1].Covers, ",") == "attempts,proof" &&
			len(parcel) == 1 && parcel[0].Field == "version" && parcel[0].Covers == nil &&
			len(doc.Types["Courier"].Fences) == 0
	}

	jsonDir := t.TempDir()
	if _, err := WriteService(schema, FormatJSON, jsonDir); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(jsonDir, "src", "dispatch.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON fenced
	if err := json.Unmarshal(written, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if !held(fromJSON) {
		t.Errorf("written JSON's fences = %s", written)
	}

	yamlDir := t.TempDir()
	if _, err := WriteService(schema, FormatYAML, yamlDir); err != nil {
		t.Fatal(err)
	}
	if written, err = os.ReadFile(filepath.Join(yamlDir, "src", "dispatch.schema.yaml")); err != nil {
		t.Fatal(err)
	}
	var fromYAML fenced
	if err := yaml.Unmarshal(written, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !held(fromYAML) {
		t.Errorf("written YAML's fences = %s", written)
	}
}
