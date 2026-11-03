package verify

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkFileTransfers holds FileUpload<C> fields and FileDownload<C> results
// (D81) to what the servers and SDKs carry, in every form. A FileUpload
// field is a single string field with no other rule, of a type that is
// only ever an operation's input, which travels as a multipart body: the
// argument is the input of a POST, PUT or PATCH, single, and no other type
// or result holds the type. A FileDownload sits on an operation's result:
// never on a field, never null, and never encrypted or paged, which answer
// JSON.
func checkFileTransfers(schema *ir.Schema, r *Result) {
	uploads := map[string]string{} // type -> its first FileUpload field
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			td := types[name]
			if td == nil {
				continue
			}
			for _, field := range td.Fields {
				if field == nil {
					continue
				}
				owner := td.Name + "." + field.Name
				if field.FileDownload != nil {
					r.errorf("", "%s: FileDownload on a field; it marks an operation's result", owner)
				}
				if field.FileUpload == nil {
					continue
				}
				if schema.Kind != ir.SchemaKindAPI {
					r.errorf("", "%s: FileUpload in a %s schema; a file is a field of an API operation's input", owner, schema.Kind)
				}
				if _, seen := uploads[td.Name]; !seen {
					uploads[td.Name] = field.Name
				}
				checkFileUploadField(owner, field, r)
			}
		}
	}

	// A type that holds a file travels only as an operation's input.
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			td := types[name]
			if td == nil {
				continue
			}
			for _, field := range td.Fields {
				if field == nil {
					continue
				}
				if file, ok := uploads[field.TypeRef.Name]; ok {
					r.errorf("", "%s.%s: holds %s, whose %s is a FileUpload; a type with a file is only an operation's input", td.Name, field.Name, field.TypeRef.Name, file)
				}
			}
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			owner := set.Name + "." + op.Name
			if op.FileUpload != nil {
				r.errorf("", "%s: FileUpload on an operation; it marks a field of the operation's input type", owner)
			}
			if file, ok := uploads[op.TypeRef.Name]; ok {
				r.errorf("", "%s: answers %s, whose %s is a FileUpload; a type with a file is only an operation's input", owner, op.TypeRef.Name, file)
			}
			for _, arg := range op.Arguments {
				if arg == nil {
					continue
				}
				file, ok := uploads[arg.TypeRef.Name]
				if !ok {
					continue
				}
				switch method := strings.ToUpper(op.HTTPMethod); {
				case arg.IsQuery || arg.TypeRef.IsArray || arg.TypeRef.IsMap:
					r.errorf("", "%s(%s): %s holds a file (%s), so it is the operation's single input, the multipart body", owner, arg.Name, arg.TypeRef.Name, file)
				case method != "POST" && method != "PUT" && method != "PATCH":
					r.errorf("", "%s: its input %s holds a file (%s), which travels as a multipart body: the operation is a POST, PUT or PATCH, not %s", owner, arg.TypeRef.Name, file, methodText(method))
				}
			}
			if op.FileDownload != nil {
				checkFileDownload(owner, set, op, r)
			}
		}
	}
}

// checkFileUploadField is checkFileTransfers for one FileUpload field.
func checkFileUploadField(owner string, field *ir.FieldDef, r *Result) {
	if field.TypeRef != (ir.TypeRef{Name: "string"}) {
		r.errorf("", "%s: a FileUpload is one file, a string field, not %s", owner, typeRefText(field.TypeRef))
	}
	for _, err := range ir.ValidateFileUploadConfig(owner, field.FileUpload) {
		r.errorf("", "%v", err)
	}
	if field.Default != nil || field.ValidateMin != nil || field.ValidateMax != nil ||
		field.ValidateMinLength != nil || field.ValidateMaxLength != nil || field.ValidatePattern != "" ||
		field.ValidateListMin != nil || field.ValidateListMax != nil || field.ValidateUploadMaxBytes != nil {
		r.errorf("", "%s: a FileUpload takes its limit in its maxBytes, and no default or other rule", owner)
	}
}

// checkFileDownload is checkFileTransfers for one FileDownload result.
func checkFileDownload(owner string, set *ir.OperationSet, op *ir.FieldDef, r *Result) {
	if !op.Required {
		r.errorf("", "%s: a FileDownload result is never null; answer 404 for a file there is not", owner)
	}
	if set.Encrypted || op.Encrypted {
		r.errorf("", "%s: a FileDownload result is the file's bytes, which no encrypted envelope carries", owner)
	}
	if op.Page != nil {
		r.errorf("", "%s: @paginated on a FileDownload result, which answers no JSON list", owner)
	}
	if op.HTTPBinaryStream != nil {
		r.errorf("", "%s: both a FileDownload and an httpBinaryStream result", owner)
	}
	if op.MCP != nil && !op.MCP.Hidden {
		r.errorf("", "%s: a visible @mcp on a FileDownload result, which is no tool result; declare @mcp({ hidden: true, reason }) or no @mcp, and the tool documents list it hidden", owner)
	}
}

// methodText names an operation's method for an error, or that it has none.
func methodText(method string) string {
	if method == "" {
		return "one without a method"
	}
	return method
}

// typeRefText writes a type reference as the TypeScript form declares it.
func typeRefText(ref ir.TypeRef) string {
	text := ref.Name
	for range ref.ArrayDepth() {
		text += "[]"
	}
	if ref.IsMap {
		text = "Record<string, " + text + ">"
	}
	return text
}
