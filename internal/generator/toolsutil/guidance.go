package toolsutil

import (
	"maps"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
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

// PageSentence is what a paged operation's tool description adds (D60).
const PageSentence = "Pages: pass the result's `next` back as `cursor` for the next page; `next` is null after the last."

// SortedPageSentence is what a sorted paged operation's tool description
// adds in place of PageSentence (D60, amended): a cursor holds the sort it
// was returned under, so the next page asks for the same one. param is
// the sort argument's tool parameter ("sort").
func SortedPageSentence(param string) string {
	return "Pages: pass the result's `next` back as `cursor`, with the same `" + param + "`, for the next page; `next` is null after the last."
}

// PagedDescription is a tool's description with PageSentence after it when
// the endpoint pages, SortedPageSentence when its page sorts, and the
// description as it is otherwise.
func PagedDescription(description string, page *apigen.PageInfo) string {
	if page == nil {
		return description
	}
	sentence := PageSentence
	if page.Sort != nil {
		// The SDKs key a tool's parameters by the argument's camelCase name.
		sentence = SortedPageSentence(tsutil.ToCamelCase(page.Sort.Param))
	}
	if description == "" {
		return sentence
	}
	return strings.TrimRight(description, " ") + " " + sentence
}
