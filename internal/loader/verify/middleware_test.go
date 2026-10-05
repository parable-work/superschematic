package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestMiddlewareBelowOneIsRefused: a schema authored as IR can carry a
// @rateLimit, @bodyLimit or @timeout the TypeScript reader would refuse.
func TestMiddlewareBelowOneIsRefused(t *testing.T) {
	zero, negative, one := 0, -1, 1
	schema := ir.NewSchema("shop", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{{
		Name:       "OrderQueries",
		Middleware: &ir.MiddlewareConfig{RateLimit: &zero, BodyLimit: &one},
		Operations: []*ir.FieldDef{
			{Name: "getOrder", Middleware: &ir.MiddlewareConfig{Timeout: &negative, BodyLimit: &zero}},
			{Name: "listOrders", Middleware: &ir.MiddlewareConfig{RateLimit: &one, Timeout: &one}},
			{Name: "countOrders"},
		},
	}}
	r := &Result{}
	checkMiddleware(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"OrderQueries: @rateLimit requestsPerMinute must be at least 1, not 0",
		"OrderQueries.getOrder: @bodyLimit megabytes must be at least 1, not 0",
		"OrderQueries.getOrder: @timeout seconds must be at least 1, not -1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}
}
