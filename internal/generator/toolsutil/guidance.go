package toolsutil

import (
	"maps"

	ir "github.com/parable-work/superschematic/ir"
)

// OperationGuidance returns an operation's @docs guidance as the tool
// documents write it: every member present, errors an empty list when none.
func OperationGuidance(docs *ir.OperationDocs) ir.ToolOperationGuidance {
	guidance := ir.ToolOperationGuidance{Errors: []ir.ToolOperationGuidanceError{}}
	if docs == nil {
		return guidance
	}
	guidance.UseWhen = docs.UseWhen
	guidance.DoNotUseWhen = docs.DoNotUseWhen
	guidance.Success = docs.Success
	for _, docError := range docs.Errors {
		guidance.Errors = append(guidance.Errors, ir.ToolOperationGuidanceError(docError))
	}
	return guidance
}

// MCPWithGuidance returns a copy of a visible tool's resolved @mcp record
// whose _meta also carries the guidance under key, so an MCP client that
// reads _meta sees it. nil, a hidden record and an empty key are returned
// unchanged.
func MCPWithGuidance(mcp *ir.OperationMCP, guidance ir.ToolOperationGuidance, key string) *ir.OperationMCP {
	if mcp == nil || mcp.Hidden || key == "" {
		return mcp
	}
	copied := *mcp
	copied.Meta = maps.Clone(mcp.Meta)
	if copied.Meta == nil {
		copied.Meta = make(map[string]any, 1)
	}
	copied.Meta[key] = guidance
	return &copied
}

// ReplayMode, IdempotencyKeyPointers and ExpectedRevisionPointers read an
// operation's @docs replay keys; an operation without @docs has none.
func ReplayMode(docs *ir.OperationDocs) string {
	if docs == nil {
		return ""
	}
	return string(docs.ReplayMode)
}

// IdempotencyKeyPointers returns a copy of the @docs idempotency pointers.
func IdempotencyKeyPointers(docs *ir.OperationDocs) []string {
	if docs == nil {
		return nil
	}
	return append([]string(nil), docs.IdempotencyKeyPointers...)
}

// ExpectedRevisionPointers returns a copy of the @docs revision pointers.
func ExpectedRevisionPointers(docs *ir.OperationDocs) []string {
	if docs == nil {
		return nil
	}
	return append([]string(nil), docs.ExpectedRevisionPointers...)
}
