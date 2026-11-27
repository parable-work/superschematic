package loader

import (
	"reflect"
	"strings"
	"testing"
)

const (
	variantFieldDBFixture  = "tsreader/testdata/services/fixture-variant-field-db"
	variantFieldAPIFixture = "tsreader/testdata/services/fixture-variant-field-api"
)

// TestVariantFieldFixtures loads the fixture pair whose Step holds a
// discriminated-union field (D149): the DB's StepResult is a union by
// kind, a member for each of two of its values, and the API's views name
// it beside their own kind.
func TestVariantFieldFixtures(t *testing.T) {
	db, err := LoadService(variantFieldDBFixture)
	if err != nil {
		t.Fatalf("load %s: %v", variantFieldDBFixture, err)
	}
	u := db.Unions["StepResult"]
	if u == nil {
		t.Fatalf("StepResult is not a union: %v", db.Unions)
	}
	if u.By != "kind" || !reflect.DeepEqual(u.Types, []string{"VerifyResult", "ReviewResult"}) ||
		!reflect.DeepEqual(u.Arms, map[string]string{"verify": "VerifyResult", "review": "ReviewResult"}) {
		t.Fatalf("StepResult = %+v", u)
	}
	if _, err := LoadService(variantFieldAPIFixture); err != nil {
		t.Fatalf("load %s: %v", variantFieldAPIFixture, err)
	}
}

// variantDoc is a General document whose Step holds a result of the variant
// union StepResult beside its kind, with fields and unions to replace.
func variantDoc(stepFields, extra string) string {
	if stepFields == "" {
		stepFields = `{"name": "kind", "typeRef": {"name": "StepKind"}, "required": true},
				{"name": "result", "typeRef": {"name": "StepResult"}}`
	}
	return `{
	"enums": {
		"StepKind": {"name": "StepKind", "values": [{"name": "Verify", "serializedAs": "verify"}, {"name": "Note", "serializedAs": "note"}]}
	},
	"unions": {
		"StepResult": {"name": "StepResult", "types": ["VerifyResult"], "by": "kind", "arms": {"verify": "VerifyResult"}}
	},
	"types": {
		"VerifyResult": {"name": "VerifyResult", "role": "EmbeddedStruct", "fields": [{"name": "passed", "typeRef": {"name": "boolean"}, "required": true}]},
		"Step": {"name": "Step", "role": "EmbeddedStruct", "fields": [
				` + stepFields + `
		]}
	}` + extra + `
}`
}

// TestVariantFieldDataForm reads a variant union from the data form, and
// refuses each way a variant field breaks D149's rules.
func TestVariantFieldDataForm(t *testing.T) {
	load := func(t *testing.T, doc string) error {
		t.Helper()
		dir := writeService(t, map[string]string{"schema.config.json": minimalConfig, "src/step.schema.json": doc})
		_, err := LoadService(dir)
		return err
	}
	t.Run("valid", func(t *testing.T) {
		dir := writeService(t, map[string]string{"schema.config.json": minimalConfig, "src/step.schema.json": variantDoc("", "")})
		schema, err := LoadService(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := schema.Unions["StepResult"]; got.By != "kind" || got.Arms["verify"] != "VerifyResult" {
			t.Fatalf("StepResult = %+v", got)
		}
	})
	for _, tc := range []struct{ name, doc, want string }{
		{
			name: "list",
			doc: variantDoc(`{"name": "kind", "typeRef": {"name": "StepKind"}, "required": true},
				{"name": "result", "typeRef": {"name": "StepResult", "isArray": true}}`, ""),
			want: "is a single value, not a list or a map",
		},
		{
			name: "no sibling",
			doc:  variantDoc(`{"name": "result", "typeRef": {"name": "StepResult"}}`, ""),
			want: "which Step has none of",
		},
		{
			name: "sibling a number",
			doc: variantDoc(`{"name": "kind", "typeRef": {"name": "number"}},
				{"name": "result", "typeRef": {"name": "StepResult"}}`, ""),
			want: "must be a string or an enum",
		},
		{
			name: "arm no member of the enum",
			doc: strings.Replace(variantDoc("", ""), `"arms": {"verify": "VerifyResult"}`,
				`"arms": {"verify": "VerifyResult", "review": "VerifyResult"}`, 1),
			want: `arm "review" is no value`,
		},
		{
			name: "member named by no arm",
			doc: strings.Replace(variantDoc("", ""), `"types": ["VerifyResult"]`,
				`"types": ["VerifyResult", "Step"]`, 1),
			want: "is named by no arm",
		},
		{
			name: "arms without by",
			doc:  strings.Replace(variantDoc("", ""), `"by": "kind", `, "", 1),
			want: "has arms and no by",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := load(t, tc.doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one that says %q", err, tc.want)
			}
		})
	}
}

// TestVariantFieldAPIRefusals: an operation's result and an argument are
// never of a variant union, whose member only a sibling field names.
func TestVariantFieldAPIRefusals(t *testing.T) {
	config := `{"name": "temp-api", "kind": "API", "outputs": {}}`
	for _, tc := range []struct{ name, op, want string }{
		{
			name: "result",
			op:   `{"name": "lastResult", "httpMethod": "GET", "typeRef": {"name": "StepResult"}}`,
			want: "answers the variant union StepResult",
		},
		{
			name: "argument",
			op:   `{"name": "record", "httpMethod": "POST", "typeRef": {"name": "boolean"}, "arguments": [{"name": "result", "typeRef": {"name": "StepResult"}, "required": true}]}`,
			want: "argument result is of the variant union StepResult",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := strings.ReplaceAll(variantDoc("", `,
	"operationSets": [{"name": "Steps", "operations": [`+tc.op+`]}]`), `"EmbeddedStruct"`, `"APIView"`)
			dir := writeService(t, map[string]string{"schema.config.json": config, "src/step.schema.json": doc})
			_, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one that says %q", err, tc.want)
			}
		})
	}
}
