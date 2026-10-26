package loader

import (
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestVerifyVectorsTS: a vector index pgvector does not build fails the
// load, naming the fix (D84).
func TestVerifyVectorsTS(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-vectors-db"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Doc: @index(["embedding"]) sets lists, which is the ivfflat method's`,
		`Doc: @index(["embedding"]) m is 1; pgvector takes 2 to 100`,
		`Doc: @index(["embedding"]) uses the hnsw method, whose ops names the metric its nearest-neighbour queries order by: vector_cosine_ops, vector_l2_ops, vector_ip_ops`,
		`uses the ivfflat method, which indexes one vector column; list one key`,
		`efConstruction is 8; pgvector takes 4 to 1000, and at least twice m (16)`,
		`Doc.wide, a vector(4096); pgvector indexes vectors of at most 2000 dimensions`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
}

// TestVerifyDigitRulesJSON: Validate's precision, scale and dimensions on
// a field of another scalar, out of range, on a map or a list of lists, or
// a scale with no precision, fail the load in every form (D84).
func TestVerifyDigitRulesJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/doc.schema.json": `{"scalars": {"Generic.Decimal": {"name": "Generic.Decimal", "languagePrimitive": "string"}, "Embedding.Vector": {"name": "Embedding.Vector", "languagePrimitive": "string"}},
		"types": {"Doc": {"name": "Doc", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "label", "typeRef": {"name": "string"}, "validatePrecision": 4},
			{"name": "rate", "typeRef": {"name": "Generic.Decimal"}, "validatePrecision": 3, "validateScale": 5},
			{"name": "ratio", "typeRef": {"name": "Generic.Decimal"}, "validateScale": 2},
			{"name": "huge", "typeRef": {"name": "Generic.Decimal"}, "validatePrecision": 1001},
			{"name": "amount", "typeRef": {"name": "Generic.Decimal"}, "validateDimensions": 2},
			{"name": "flat", "typeRef": {"name": "Embedding.Vector"}, "validateDimensions": 0},
			{"name": "grid", "typeRef": {"name": "Generic.Decimal", "isArray": true, "isArrayOfArrays": true}, "validatePrecision": 4}
		]}}}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Doc.label: Validate's precision and scale bound a Generic.Decimal, not string; declare it Validate<Generic.Decimal, { precision, scale }>`,
		`Doc.rate: Validate's scale 5 is more than its precision 3`,
		`Doc.ratio: Validate's scale needs a precision`,
		`Doc.huge: Validate's precision is 1001; it must be from 1 to 1000`,
		`Doc.amount: Validate's dimensions bound an Embedding.Vector, not Generic.Decimal`,
		`Doc.flat: Validate's dimensions is 0; it must be from 1 to 16000`,
		`Doc.grid: Validate's precision, scale and dimensions bound a single value or a list of them, not a map or a list of lists`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
}

// TestVerifyVectorColumnsJSON: a table's Embedding.Vector needs dimensions,
// and a list of them is no column, while JSON holds either (D84).
func TestVerifyVectorColumnsJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/doc.schema.json": `{"scalars": {"Embedding.Vector": {"name": "Embedding.Vector", "languagePrimitive": "string"}},
		"types": {"Doc": {"name": "Doc", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "bare", "typeRef": {"name": "Embedding.Vector"}, "required": true},
			{"name": "many", "typeRef": {"name": "Embedding.Vector", "isArray": true}, "validateDimensions": 3},
			{"name": "kept", "typeRef": {"name": "Embedding.Vector", "isArray": true}, "jsonField": true},
			{"name": "loose", "typeRef": {"name": "Embedding.Vector"}, "jsonField": true}
		]}}}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Doc.bare is an Embedding.Vector with no dimensions, so its column has no type: declare them, Validate<Embedding.Vector, { dimensions: 1024 }>, a vector(1024) column`,
		`Doc.many is a list of Embedding.Vector, which no column holds`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
	for _, field := range []string{"Doc.kept", "Doc.loose"} {
		if strings.Contains(err.Error(), field) {
			t.Errorf("error %q refuses %s, which JSON holds", err.Error(), field)
		}
	}
}

// TestLoadVectorsFixture: the fixture's digit rules and vector indexes load
// as declared.
func TestLoadVectorsFixture(t *testing.T) {
	schema, err := LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-vectors-db"))
	if err != nil {
		t.Fatal(err)
	}
	rate := fieldNamed(schema.Types["LedgerLine"].Fields, "rate")
	if rate == nil || rate.ValidatePrecision == nil || *rate.ValidatePrecision != 10 || rate.ValidateScale == nil || *rate.ValidateScale != 4 {
		t.Errorf("LedgerLine.rate = %+v", rate)
	}
	embedding := fieldNamed(schema.Types["TextEmbedding"].Fields, "embedding")
	if embedding == nil || embedding.ValidateDimensions == nil || *embedding.ValidateDimensions != 1024 {
		t.Errorf("TextEmbedding.embedding = %+v", embedding)
	}
	indexes := schema.Types["TextEmbedding"].Indexes
	if len(indexes) != 1 || indexes[0].Method != "hnsw" || indexes[0].Ops != "vector_cosine_ops" || indexes[0].M == nil || *indexes[0].M != 16 || indexes[0].EFConstruction == nil || *indexes[0].EFConstruction != 64 {
		t.Errorf("TextEmbedding's indexes = %+v", indexes)
	}
}

func fieldNamed(fields []*ir.FieldDef, name string) *ir.FieldDef {
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}
