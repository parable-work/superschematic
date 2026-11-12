package verify

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A table operation documents itself (D111). An operation the build serves
// from a table says what it does in its declaration: its method, its kind
// and its table. So one that declares no @docs is given them, and one that
// declares no @mcp is a tool, as an operation the loader adds for a
// behavior is (D103, behaviorops): a set of five such operations is five
// @rest lines and no more.
//
//   - @docs: the title is the method's name as words ("List issues" for
//     listIssues); the description the method's comment, or a sentence of
//     its kind and its table without one; the capability the set and the
//     method ("issues.list-issues"); active, public and mapped; and a read
//     and a list replay as read-only.
//   - @mcp: a visible tool whose handle is the method's name in snake_case
//     (list_issues), with the policy's default invocation. An operation
//     only a calling service reaches is hidden, with that reason (D37), and
//     so is one whose payload is encrypted, which no tool call seals.
//
// A name another operation of the API holds takes its set's before it:
// two sets that each declare list are teams_list and members_list, "Teams:
// list" and "Members: list", since an API's tools are told apart by their
// handles and their names. What the operation declares is left as it is,
// so @docs and @mcp still say otherwise, and @mcp({ hidden: true, reason })
// keeps one out of the tools.

// documentTableOperations is the pass over schema's operations the loader
// filled as table operations.
func documentTableOperations(schema *ir.Schema, policy registry.ToolInvocationPolicy) {
	type marked struct {
		set *ir.OperationSet
		op  *ir.FieldDef
	}
	var ops []marked
	// The names, the handles and the titles the API's operations declare,
	// which a default must not repeat.
	names := map[string]int{}
	handles := map[string]bool{}
	titles := map[string]bool{}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			names[strings.ToLower(op.Name)]++
			if op.MCP != nil && !op.MCP.Hidden && op.MCP.Handle != "" {
				handles[op.MCP.Handle] = true
			}
			if op.Docs != nil {
				titles[strings.ToLower(op.Docs.Title)] = true
			}
			if op.TableOperation != nil && op.TableOperation.Kind != "" {
				ops = append(ops, marked{set, op})
			}
		}
	}
	for _, m := range ops {
		set, op := m.set, m.op
		handle, title := codegen.ToSnakeCase(op.Name), sentence(words(op.Name))
		if names[strings.ToLower(op.Name)] > 1 || (op.MCP == nil && handles[handle]) || (op.Docs == nil && titles[strings.ToLower(title)]) {
			handle = codegen.ToSnakeCase(set.Name) + "_" + handle
			title = set.Name + ": " + words(op.Name)
		}
		if op.Docs == nil {
			// A comment's lines are one paragraph of a description.
			description := strings.Join(strings.Fields(op.Comment), " ")
			if description == "" {
				description = tableOperationSentence(op.TableOperation)
			}
			op.Docs = &ir.OperationDocs{
				Title:         title,
				Description:   description,
				Capability:    codegen.ToKebabCase(set.Name) + "." + codegen.ToKebabCase(op.Name),
				Lifecycle:     ir.DocsLifecycleActive,
				Visibility:    ir.DocsVisibilityPublic,
				MappingStatus: ir.DocsMappingStatusMapped,
			}
			if op.TableOperation.Kind == ir.TableOperationRead || op.TableOperation.Kind == ir.TableOperationList {
				op.Docs.ReplayMode = ir.DocsReplayModeReadOnly
			}
			titles[strings.ToLower(title)] = true
		}
		if op.MCP != nil {
			continue
		}
		switch callers := ir.EffectiveServiceCallers(set, op); {
		case callers != nil && callers.Mode == ir.ServiceCallersRequire:
			op.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: "Only the services that @requireService admits call it (D37)."}
		case mcpPayloadEncrypted(set, op):
			op.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: "Its payload is encrypted, which only the SDK seals."}
		default:
			op.MCP = &ir.OperationMCP{Handle: handle, Invocation: ir.MCPInvocation{Key: policy.Key, Value: policy.Default}}
			handles[handle] = true
		}
	}
}

// tableOperationSentence says what a table operation does, for one whose
// method has no comment.
func tableOperationSentence(to *ir.TableOperation) string {
	switch to.Kind {
	case ir.TableOperationList:
		return "Lists the " + to.Type + " rows, a page at a time."
	case ir.TableOperationRead:
		return "Reads the " + to.Type + " its key names."
	case ir.TableOperationCreate:
		return "Creates a " + to.Type + "."
	case ir.TableOperationUpdate:
		return "Updates the " + to.Type + " its key names: a member left out stays as it is."
	}
	return "Deletes the " + to.Type + " its key names."
}

// words writes a camelCase name as lowercase words: listIssues, list
// issues.
func words(name string) string {
	return strings.ReplaceAll(codegen.ToKebabCase(name), "-", " ")
}

// sentence is text with its first letter in upper case.
func sentence(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
