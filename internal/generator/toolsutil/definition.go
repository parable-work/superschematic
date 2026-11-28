package toolsutil

import (
	"crypto/sha256"
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// ToolOperation is what a tool definition says of its operation the same
// way in every SDK: its @docs and @mcp records as the tool documents write
// them, the permissions it requires, whether an MCP client can bind it,
// and the digest of its arguments' schema. The SDK generators' tool
// definitions embed it.
type ToolOperation struct {
	Title                    string                   // @docs title
	MCP                      *ir.OperationMCP         // resolved @mcp record, _meta with the guidance; nil without @mcp
	Capability               string                   // @docs capability
	Lifecycle                string                   // @docs lifecycle
	Visibility               string                   // @docs visibility
	Audience                 string                   // @docs audience
	Guidance                 ir.ToolOperationGuidance // @docs guidance
	ReplayMode               string                   // @docs replay mode
	IdempotencyKeyPointers   []string                 // pointers into the parameters
	ExpectedRevisionPointers []string                 // pointers into the parameters
	RequiredPerms            []string                 // permissions the route requires
	BindingStatus            string                   // ready, or unsupported_multipart for a file upload
	InputSchemaDigest        string                   // sha256 of the encoded parameters
}

// NewToolOperation derives a tool's ToolOperation from its operation's
// title, @docs and @mcp records and required permissions, whether it
// uploads a file, and its parameters' JSON Schema, which the digest hashes
// as the keys say.
func NewToolOperation(
	title string,
	docs *ir.OperationDocs,
	mcp *ir.OperationMCP,
	requiredPerms []string,
	hasFileUpload bool,
	parameters JSONSchemaObject,
	keys apigen.ToolKeys,
) ToolOperation {
	parametersJSON, _ := parameters.DigestJSON(keys.DigestInternalTypes)
	bindingStatus := "ready"
	if hasFileUpload {
		bindingStatus = "unsupported_multipart"
	}
	guidance := OperationGuidance(docs)
	operation := ToolOperation{
		Title:                    title,
		MCP:                      MCPWithGuidance(mcp, guidance, keys.Guidance),
		Guidance:                 guidance,
		ReplayMode:               ReplayMode(docs),
		IdempotencyKeyPointers:   IdempotencyKeyPointers(docs),
		ExpectedRevisionPointers: ExpectedRevisionPointers(docs),
		RequiredPerms:            append([]string(nil), requiredPerms...),
		BindingStatus:            bindingStatus,
		InputSchemaDigest:        fmt.Sprintf("sha256:%x", sha256.Sum256(parametersJSON)),
	}
	if docs != nil {
		operation.Capability = docs.Capability
		operation.Lifecycle = string(docs.Lifecycle)
		operation.Visibility = string(docs.Visibility)
		operation.Audience = string(docs.Audience)
	}
	return operation
}

// ToolDescription is a tool's description: the operation's description,
// else "<name> endpoint", noting that it requires authentication when no
// @docs record describes it, then PagedDescription's and
// FilteredDescription's sentences.
func ToolDescription(description, name string, docs *ir.OperationDocs, requiresAuth bool, page *apigen.PageInfo, filter *apigen.FilterInfo) string {
	if description == "" {
		description = fmt.Sprintf("%s endpoint", name)
	}
	if docs == nil && requiresAuth {
		description = fmt.Sprintf("%s (Requires authentication)", description)
	}
	description = PagedDescription(description, page)
	return FilteredDescription(description, filter, page)
}

// NewToolScalarArg is a body argument as a tool argument, keyed by its
// camelCase name. Its schema is T, T[] or T[][], or a map of T or T[].
func NewToolScalarArg(arg apigen.Param) ToolScalarArg {
	return ToolScalarArg{
		Name:            arg.Name,
		TSName:          tsutil.ToCamelCase(arg.Name),
		Type:            arg.Type,
		Required:        arg.Required,
		IsArray:         arg.IsArray,
		IsArrayOfArrays: arg.IsArrayOfArrays,
		IsMap:           arg.IsMap,
		Reference:       arg.Reference,
		Search:          arg.Search,
	}
}
