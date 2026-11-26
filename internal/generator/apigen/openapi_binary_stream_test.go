package apigen_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestOpenAPIBinaryStream: in fixture-stream-api's OpenAPI document the 200
// of exportReport, an @httpBinaryStream operation (D167), is the stream's
// media type, a binary string, with the X-Export-Id header it declares, and
// no envelope. Its refusals stay problems, and describeReport beside it
// answers the envelope.
func TestOpenAPIBinaryStream(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-stream-api"))
	if err != nil {
		t.Fatalf("load fixture-stream-api: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "fixture-stream-api"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema map[string]any `json:"schema"`
				} `json:"content"`
				Headers map[string]any `json:"headers"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &doc); err != nil {
		t.Fatal(err)
	}

	export := doc.Paths["/api/reports/{reportId}/export"]["post"]
	success, ok := export.Responses["200"]
	if !ok {
		t.Fatalf("exportReport has no 200: %+v", export)
	}
	if len(success.Content) != 1 {
		t.Errorf("exportReport's 200 content = %+v, want the stream's media type alone", success.Content)
	}
	if got, want := success.Content["application/vnd.apache.arrow.stream"].Schema, map[string]any{"type": "string", "format": "binary"}; !reflect.DeepEqual(got, want) {
		t.Errorf("exportReport's 200 schema = %v, want %v", got, want)
	}
	if _, ok := success.Headers["X-Export-Id"]; !ok || len(success.Headers) != 1 {
		t.Errorf("exportReport's 200 headers = %v, want X-Export-Id", success.Headers)
	}
	for _, status := range []string{"400", "500"} {
		if _, ok := export.Responses[status].Content["application/problem+json"]; !ok {
			t.Errorf("exportReport's %s = %+v, want a problem", status, export.Responses[status])
		}
	}

	describe := doc.Paths["/api/reports/{reportId}"]["get"].Responses["200"]
	if _, ok := describe.Content["application/json"]; !ok || describe.Headers != nil {
		t.Errorf("describeReport's 200 = %+v, want the envelope", describe)
	}
}
