package tsreader

import (
	tsast "github.com/microsoft/typescript-go/shim/ast"

	ir "github.com/parable-work/superschematic/ir"
)

// astNodeList is a type reference's type arguments.
type astNodeList = tsast.NodeList

// fileUploadUsage and fileDownloadUsage are how the two file wrappers are
// written, for their errors (D81).
const (
	fileUploadUsage   = `FileUpload<C> takes a type literal of its constraints, each optional: FileUpload<{ maxBytes: 5242880; contentTypes: ["text/csv"]; extensions: [".csv"] }>`
	fileDownloadUsage = `FileDownload<C> takes a type literal naming the media type and, optionally, the filename: FileDownload<{ contentType: "text/csv"; filename: "orders.csv" }>`
)

// fileUploadFromTypeArgs reads FileUpload<C> (D81): a string field that is
// a file, a multipart part of its own, under the constraints C names. A
// bare FileUpload admits any file up to the default limit. The members are
// read from the written literal, as the User trait's config is, since the
// compiler admits a key FileUploadConfig does not declare.
func (w *walker) fileUploadFromTypeArgs(node *astNode, args *astNodeList) (*typeInfo, *SchemaError) {
	config := &ir.FileUploadConfig{}
	info := &typeInfo{ref: ir.TypeRef{Name: "string"}, isPrimitive: true, fileUpload: config}
	if args == nil || len(args.Nodes) == 0 {
		return info, nil
	}
	if len(args.Nodes) != 1 {
		return nil, errorAtNode(node, "%s", fileUploadUsage)
	}
	members, serr := w.fileConfigMembers(args.Nodes[0], fileUploadUsage)
	if serr != nil {
		return nil, serr
	}
	for _, member := range members {
		switch member.key {
		case "maxBytes":
			value, ok := literalValue(w.checker.GetTypeFromTypeNode(member.value))
			bytes, isInteger := safeIntegerLiteral(value)
			if !ok || !isInteger || bytes < 1 || bytes > int64(^uint32(0)>>1) {
				return nil, errorAtNode(member.node, "FileUpload maxBytes must be a whole number of bytes from 1 to 2147483647")
			}
			config.MaxSize = int(bytes)
		case "contentTypes":
			values, serr := stringTuple(member, "FileUpload contentTypes is a tuple of media types: [\"text/csv\"]")
			if serr != nil {
				return nil, serr
			}
			config.AllowedTypes = values
		case "extensions":
			values, serr := stringTuple(member, "FileUpload extensions is a tuple of extensions with the dot: [\".csv\"]")
			if serr != nil {
				return nil, serr
			}
			config.AllowedExtensions = values
		default:
			return nil, errorAtNode(member.node, "FileUpload has no constraint %q; it takes maxBytes, contentTypes and extensions", member.key)
		}
	}
	for _, err := range ir.ValidateFileUploadConfig("FileUpload", config) {
		return nil, errorAtNode(args.Nodes[0], "%v", err)
	}
	return info, nil
}

// fileDownloadFromTypeArgs reads FileDownload<C> (D81): an operation's
// result that is a file of the media type C names, with the filename
// Content-Disposition offers when the implementation names none. It names
// no JSON type.
func (w *walker) fileDownloadFromTypeArgs(node *astNode, args *astNodeList) (*typeInfo, *SchemaError) {
	if args == nil || len(args.Nodes) != 1 {
		return nil, errorAtNode(node, "%s", fileDownloadUsage)
	}
	members, serr := w.fileConfigMembers(args.Nodes[0], fileDownloadUsage)
	if serr != nil {
		return nil, serr
	}
	download := &ir.FileDownloadDef{}
	for _, member := range members {
		text, ok := stringLiteralType(member.value)
		switch member.key {
		case "contentType":
			if !ok {
				return nil, errorAtNode(member.node, "FileDownload contentType must be a string literal: \"text/csv\"")
			}
			download.ContentType = text
		case "filename":
			if !ok || text == "" {
				return nil, errorAtNode(member.node, "FileDownload filename must be a non-empty string literal: \"orders.csv\"")
			}
			download.Filename = text
		default:
			return nil, errorAtNode(member.node, "FileDownload has no member %q; it takes contentType and filename", member.key)
		}
	}
	if download.ContentType == "" {
		return nil, errorAtNode(args.Nodes[0], "%s", fileDownloadUsage)
	}
	for _, err := range ir.ValidateFileDownload("FileDownload", download) {
		return nil, errorAtNode(args.Nodes[0], "%v", err)
	}
	return &typeInfo{fileDownload: download}, nil
}

// fileConfigMember is one property of a file wrapper's config literal.
type fileConfigMember struct {
	key   string
	node  *astNode // the property signature
	value *astNode // its type
}

// fileConfigMembers reads a file wrapper's config: a type literal of
// required property signatures, each written once.
func (w *walker) fileConfigMembers(literal *astNode, usage string) ([]fileConfigMember, *SchemaError) {
	if literal.Kind != kindTypeLiteral {
		return nil, errorAtNode(literal, "%s", usage)
	}
	var members []fileConfigMember
	seen := map[string]bool{}
	for _, m := range literal.AsTypeLiteralNode().Members.Nodes {
		if m.Kind != kindPropertySignature {
			return nil, errorAtNode(m, "%s", usage)
		}
		key, serr := w.evaluatePropertyName(m.Name(), 0)
		if serr != nil {
			return nil, serr
		}
		sig := m.AsPropertySignatureDeclaration()
		if sig.Type == nil || (sig.PostfixToken != nil && sig.PostfixToken.Kind == kindQuestionToken) {
			return nil, errorAtNode(m, "%s", usage)
		}
		if seen[key] {
			return nil, errorAtNode(m, "%s is written twice", key)
		}
		seen[key] = true
		members = append(members, fileConfigMember{key: key, node: m, value: sig.Type})
	}
	return members, nil
}

// stringTuple reads a member's tuple of non-empty string literals, readonly
// or not.
func stringTuple(member fileConfigMember, usage string) ([]string, *SchemaError) {
	tuple := member.value
	for tuple.Kind == kindTypeOperator || tuple.Kind == kindParenthesizedType {
		if tuple.Kind == kindTypeOperator {
			tuple = tuple.AsTypeOperatorNode().Type
		} else {
			tuple = tuple.AsParenthesizedTypeNode().Type
		}
	}
	if tuple.Kind != kindTupleType {
		return nil, errorAtNode(member.node, "%s", usage)
	}
	values := []string{}
	for _, element := range tuple.Elements() {
		text, ok := stringLiteralType(element)
		if !ok || text == "" {
			return nil, errorAtNode(element, "%s", usage)
		}
		values = append(values, text)
	}
	return values, nil
}
