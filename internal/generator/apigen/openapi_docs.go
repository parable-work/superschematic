package apigen

import ir "github.com/parable-work/superschematic/ir"

// OpenAPIDocsKey is the vendor-extension key under which an operation's
// @docs record appears in the OpenAPI document. A distribution that wants
// its own key renames it with an OpenAPIHook.
const OpenAPIDocsKey = "x-superschematic-docs"

// openAPIDocsExtension renders an operation's @docs record for the OpenAPI
// vendor extension: the required keys always, the optional ones when set.
func openAPIDocsExtension(docs *ir.OperationDocs) map[string]interface{} {
	metadata := map[string]interface{}{
		"title":         docs.Title,
		"description":   docs.Description,
		"capability":    docs.Capability,
		"lifecycle":     docs.Lifecycle,
		"visibility":    docs.Visibility,
		"mappingStatus": docs.MappingStatus,
	}
	if docs.Audience != "" {
		metadata["audience"] = docs.Audience
	}
	if docs.Replacement != "" {
		metadata["replacement"] = docs.Replacement
	}
	if docs.Sunset != "" {
		metadata["sunset"] = docs.Sunset
	}
	return metadata
}
