package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkMCPToolsLeftOut warns of a visible @mcp tool that an API's MCP
// endpoint (outputs.api.mcp, D81) leaves out of tools/list, which the
// build otherwise did without a word (D93): one whose input carries a file,
// which tool arguments cannot carry, so the tool documents mark it
// unsupported_multipart and invokeTool refuses it too, and one whose
// payload is encrypted, which only the SDK seals. Each warning names the
// operation, the tool and the reason, and the fix that says so on purpose:
// a hidden @mcp with its reason. A FileDownload result with a visible @mcp
// is checkFileTransfers' error, not a warning.
func checkMCPToolsLeftOut(schema *ir.Schema, r *Result) {
	if schema.Kind != ir.SchemaKindAPI {
		return
	}
	files := map[string]string{} // input type -> its first FileUpload field
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			td := types[name]
			if td == nil {
				continue
			}
			for _, field := range td.Fields {
				if field != nil && field.FileUpload != nil {
					if _, seen := files[td.Name]; !seen {
						files[td.Name] = field.Name
					}
				}
			}
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil || op.MCP == nil || op.MCP.Hidden || op.FileDownload != nil {
				continue
			}
			owner := set.Name + "." + op.Name
			tool := op.MCP.Handle
			if tool == "" {
				tool = op.Name
			}
			uploads := false
			for _, arg := range op.Arguments {
				if arg == nil {
					continue
				}
				if file, ok := files[arg.TypeRef.Name]; ok {
					r.warnf("", "%s: the visible @mcp tool %s uploads a file (%s.%s), which tool arguments cannot carry, so an API's MCP endpoint leaves it out of tools/list and invokeTool refuses it; declare @mcp({ hidden: true, reason }) to leave it out on purpose", owner, tool, arg.TypeRef.Name, file)
					uploads = true
					break
				}
			}
			if !uploads && mcpPayloadEncrypted(set, op) {
				r.warnf("", "%s: the visible @mcp tool %s has an encrypted payload, which only the SDK seals, so an API's MCP endpoint leaves it out of tools/list; declare @mcp({ hidden: true, reason }) to leave it out on purpose", owner, tool)
			}
		}
	}
}

// mcpPayloadEncrypted reports whether an operation's payload is encrypted,
// as apigen reads it: its set or the operation is encrypted, or one of its
// arguments is an EncryptedField<T>.
func mcpPayloadEncrypted(set *ir.OperationSet, op *ir.FieldDef) bool {
	if set.Encrypted || op.Encrypted {
		return true
	}
	for _, arg := range op.Arguments {
		if arg != nil && arg.Encrypted {
			return true
		}
	}
	return false
}
