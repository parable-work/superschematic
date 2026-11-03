package tsreader

import (
	"strings"
	"testing"
)

// TestAColonPathParameterIsALocatedSchemaError (D100): @rest's path writes
// a parameter in braces. A :name segment, a parameter in Express's and
// Hono's routes, checked clean and was published and routed as its text;
// the load refuses it at the path argument, with the path rewritten.
func TestAColonPathParameterIsALocatedSchemaError(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { HttpMethod, rest } from "@superschematic/api";
export class BusinessQueries {
  @rest(HttpMethod.GET, "businesses/:businessId/export")
  exportBusiness(businessId: string): string {
    throw new Error("schema declaration only");
  }
  @rest(HttpMethod.GET, "businesses/{businessId}")
  getBusiness(businessId: string): string {
    throw new Error("schema declaration only");
  }
}
`})
	_, _, err := LoadService(dir)
	if err == nil {
		t.Fatal("expected the :businessId segment to be refused")
	}
	want := `a.schema.ts:3:25: @rest path "businesses/:businessId/export" writes a path parameter as ":businessId", which OpenAPI and the generated routers read as text; write it in braces: "businesses/{businessId}/export"`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v\nwant it to hold %s", err, want)
	}
	if strings.Count(err.Error(), "@rest path") != 1 {
		t.Errorf("only the :name path is refused: %v", err)
	}
}
