package apigen

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// PageInfo is an endpoint's @paginated (D60): the operation pages its list
// by an opaque cursor, takes the query parameters cursor and limit, and
// answers {items, next} in data.
type PageInfo struct {
	// DefaultLimit is the limit a request without one gets.
	DefaultLimit int
	// MaxLimit is the largest limit a request may send.
	MaxLimit int
	// Operation names the operation as the loader does, Set.operation, for
	// a generator's messages.
	Operation string
}

// The page's query parameters (D60), as every writer names them.
const (
	PageCursorParam = "cursor"
	PageLimitParam  = "limit"
)

// PageCursorDescription, PageLimitDescription and PageNextDescription
// describe the page's two parameters and its next in the OpenAPI document
// and the tool documents.
const (
	PageCursorDescription = "The `next` of the previous page; absent for the first page."
	PageNextDescription   = "The cursor of the next page, to send as cursor; null after the last page."
)

// PageLimitDescription is the description of a page's limit.
func PageLimitDescription(page *PageInfo) string {
	return fmt.Sprintf("The most items the page holds: 1 to %d, %d when absent.", page.MaxLimit, page.DefaultLimit)
}

// pageInfo is the endpoint's page, nil for an operation without one.
func pageInfo(set *ir.OperationSet, op *ir.FieldDef) *PageInfo {
	if op.Page == nil {
		return nil
	}
	return &PageInfo{DefaultLimit: op.Page.DefaultLimit, MaxLimit: op.Page.MaxLimit, Operation: set.Name + "." + op.Name}
}

// GoResultType is what the endpoint's Go implementation method returns
// beside its error: the HTTP runtime's paging.Page of the list's element
// for a paged operation (D60), the map for a map, the list for any other
// list, and a pointer to the output otherwise.
func (e EndpointInfo) GoResultType() string {
	switch {
	case e.Page != nil:
		return "paging.Page[" + e.OutputGoType + "]"
	case e.OutputIsMap:
		return e.OutputGoMapType()
	case e.OutputIsArray:
		return e.OutputGoListType()
	}
	return "*" + e.OutputGoType
}

// GoZeroResult is the result a Go method of GoResultType returns beside an
// error: the empty page for a paged operation, nil otherwise.
func (e EndpointInfo) GoZeroResult() string {
	if e.Page != nil {
		return e.GoResultType() + "{}"
	}
	return "nil"
}

// HasPagedEndpoints reports whether an endpoint of the output pages.
func (o *APIOutput) HasPagedEndpoints() bool {
	for _, endpoint := range o.Endpoints {
		if endpoint.Page != nil {
			return true
		}
	}
	return false
}

// pageOpenAPIParameters are a paged endpoint's two query parameters in the
// OpenAPI document, after its own: the cursor, of the unpadded base64url
// alphabet, and the limit with its bounds and default.
func pageOpenAPIParameters(page *PageInfo) []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        PageCursorParam,
			"in":          "query",
			"required":    false,
			"description": PageCursorDescription,
			"schema": map[string]interface{}{
				"type":      "string",
				"minLength": 1,
				"maxLength": ir.PageCursorMaxLength,
				"pattern":   ir.PageCursorPattern,
			},
		},
		{
			"name":        PageLimitParam,
			"in":          "query",
			"required":    false,
			"description": PageLimitDescription(page),
			"schema": map[string]interface{}{
				"type":    "integer",
				"minimum": 1,
				"maximum": page.MaxLimit,
				"default": page.DefaultLimit,
			},
		},
	}
}

// pageOpenAPISchema is the page a paged endpoint's data holds, written
// inline so no name can collide with a schema type: its items, the list
// the operation returns, and next, null after the last page.
func pageOpenAPISchema(items map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":     "object",
		"required": []string{"items", "next"},
		"properties": map[string]interface{}{
			"items": items,
			"next": map[string]interface{}{
				"type":        "string",
				"nullable":    true,
				"description": PageNextDescription,
			},
		},
	}
}
