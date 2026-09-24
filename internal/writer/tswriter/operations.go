package tswriter

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// httpMethodMembers maps IR HTTP methods to HttpMethod enum members.
var httpMethodMembers = map[string]string{
	"GET": "GET", "POST": "POST", "PUT": "PUT", "PATCH": "PATCH", "DELETE": "DELETE",
}

// emitOperationSet renders an operation-set class: a class with method
// stubs, middleware decorators, and an Authenticated or Encrypted base.
func (e *emitter) emitOperationSet(set *ir.OperationSet) {
	owner := "operation set " + set.Name
	if set.Description != "" {
		e.failf("%s: descriptions have no TypeScript authoring form", owner)
	}

	auth := false
	for i, op := range set.Operations {
		if i == 0 {
			auth = op.Auth
		} else if op.Auth != auth {
			e.failf("%s: operations mix authenticated and unauthenticated access; TypeScript carries auth on the class base", owner)
		}
	}
	if auth && set.Encrypted {
		e.failf("%s: a class extends either Authenticated or Encrypted, not both", owner)
	}

	e.body.WriteString("\n")
	e.comment("", set.Comment)
	e.emitMiddlewareDecorators("", set.Middleware)

	heritage := ""
	if auth {
		heritage = " extends " + e.use("Authenticated")
	} else if set.Encrypted {
		heritage = " extends " + e.use("Encrypted")
	}
	fmt.Fprintf(&e.body, "export class %s%s {\n", e.ident(set.Name, "operation set"), heritage)

	for i, op := range set.Operations {
		if i > 0 {
			e.body.WriteString("\n")
		}
		e.emitOperation(set.Name, op)
	}
	e.body.WriteString("}\n")
}

// emitOperation renders one operation method stub with its decorators.
func (e *emitter) emitOperation(setName string, op *ir.FieldDef) {
	owner := fmt.Sprintf("%s.%s", setName, op.Name)
	e.checkOperationField(op, owner)

	e.comment("  ", op.Comment)
	if op.Docs != nil {
		fmt.Fprintf(&e.body, "  @%s(%s)\n", e.useAs("@superschematic/api", "docs", "apiDocs"), operationDocsLiteral(op.Docs))
	}
	if op.Icon != "" {
		fmt.Fprintf(&e.body, "  @%s(%s)\n", e.useAs("@superschematic/api", "icon", "apiIcon"), quote(op.Icon))
	}
	if op.MCP != nil {
		fmt.Fprintf(&e.body, "  @%s(%s)\n", e.use("mcp"), operationMCPLiteral(op.MCP))
	}

	if op.HTTPMethod != "" {
		member, ok := httpMethodMembers[op.HTTPMethod]
		if !ok {
			e.failf("%s: unsupported HTTP method %q", owner, op.HTTPMethod)
			member = "GET"
		}
		args := fmt.Sprintf("%s.%s", e.use("HttpMethod"), member)
		if op.RestPath != "" {
			args += ", " + quote(op.RestPath)
		}
		fmt.Fprintf(&e.body, "  @%s(%s)\n", e.use("rest"), args)
	} else if op.RestPath != "" {
		e.failf("%s: a REST path without an HTTP method has no TypeScript authoring form", owner)
	}
	if len(op.Permissions) > 0 {
		perms := make([]string, len(op.Permissions))
		for i, p := range op.Permissions {
			perms[i] = quote(p)
		}
		fmt.Fprintf(&e.body, "  @%s([%s])\n", e.use("requirePermission"), strings.Join(perms, ", "))
	}
	if op.RequireOwnership {
		fmt.Fprintf(&e.body, "  @%s\n", e.use("requireOwnership"))
	}
	if op.ManualRouteRegistration {
		fmt.Fprintf(&e.body, "  @%s\n", e.use("manualRouteRegistration"))
	}
	e.emitMiddlewareDecorators("  ", op.Middleware)

	params := make([]string, 0, len(op.Arguments))
	for _, arg := range op.Arguments {
		argOwner := fmt.Sprintf("%s(%s)", owner, arg.Name)
		if arg.Comment != "" || arg.Description != "" {
			e.failf("%s: argument comments and descriptions have no TypeScript authoring form", argOwner)
		}
		params = append(params, fmt.Sprintf("%s: %s", e.ident(arg.Name, "argument"), e.argumentTypeExpr(arg, argOwner)))
	}

	returnExpr := e.operationReturnExpr(op, owner)
	fmt.Fprintf(&e.body, "  %s(%s): %s {\n", e.ident(op.Name, "operation"), strings.Join(params, ", "), returnExpr)
	e.body.WriteString("    throw new Error(\"schema declaration only\");\n  }\n")
}

// operationReturnExpr renders an operation's return type expression.
func (e *emitter) operationReturnExpr(op *ir.FieldDef, owner string) string {
	if op.TypeRef.IsMap {
		e.failf("%s: map-typed returns have no TypeScript authoring form", owner)
		return "never"
	}
	expr := e.renderTypeName(op.TypeRef.Name, owner)
	if op.TypeRef.IsArray {
		expr += "[]"
	}
	if op.Encrypted {
		expr = fmt.Sprintf("%s<%s>", e.use("EncryptedField"), expr)
	}
	if !op.Required {
		expr = fmt.Sprintf("%s<%s>", e.use("Nullable"), expr)
	}
	return expr
}

// emitMiddlewareDecorators renders @rateLimit/@bodyLimit/@timeout.
func (e *emitter) emitMiddlewareDecorators(indent string, mw *ir.MiddlewareConfig) {
	if mw == nil {
		return
	}
	if mw.RateLimit != nil {
		fmt.Fprintf(&e.body, "%s@%s({ requestsPerMinute: %d })\n", indent, e.use("rateLimit"), *mw.RateLimit)
	}
	if mw.BodyLimit != nil {
		fmt.Fprintf(&e.body, "%s@%s({ megabytes: %d })\n", indent, e.use("bodyLimit"), *mw.BodyLimit)
	}
	if mw.Timeout != nil {
		fmt.Fprintf(&e.body, "%s@%s({ seconds: %d })\n", indent, e.use("timeout"), *mw.Timeout)
	}
}

// checkOperationField rejects FieldDef metadata that has no authoring form
// on an operation method.
func (e *emitter) checkOperationField(op *ir.FieldDef, owner string) {
	rest := *op
	rest.Name = ""
	rest.Comment = ""
	rest.Docs = nil
	rest.MCP = nil
	rest.Icon = ""
	rest.TypeRef = ir.TypeRef{}
	rest.Required = false
	rest.Auth = false
	rest.Encrypted = false
	rest.HTTPMethod = ""
	rest.RestPath = ""
	rest.Permissions = nil
	rest.RequireOwnership = false
	rest.ManualRouteRegistration = false
	rest.Middleware = nil
	rest.Arguments = nil
	if !reflect.DeepEqual(&rest, &ir.FieldDef{}) {
		e.failf("%s: carries metadata with no TypeScript authoring form on an operation", owner)
	}
}

// operationDocsLiteral renders an @docs record as the object literal the
// decorator takes. Optional keys are written only when set, and
// mappingStatus only when it is not the default.
func operationDocsLiteral(docs *ir.OperationDocs) string {
	parts := []string{
		"title: " + quote(docs.Title),
		"description: " + quote(docs.Description),
		"capability: " + quote(docs.Capability),
		"lifecycle: " + quote(string(docs.Lifecycle)),
		"visibility: " + quote(string(docs.Visibility)),
	}
	if docs.Audience != "" {
		parts = append(parts, "audience: "+quote(string(docs.Audience)))
	}
	if docs.MappingStatus != "" && docs.MappingStatus != ir.DocsMappingStatusMapped {
		parts = append(parts, "mappingStatus: "+quote(string(docs.MappingStatus)))
	}
	if docs.Replacement != "" {
		parts = append(parts, "replacement: "+quote(docs.Replacement))
	}
	if docs.Sunset != "" {
		parts = append(parts, "sunset: "+quote(docs.Sunset))
	}
	if docs.ReplayMode != "" {
		parts = append(parts, "replayMode: "+quote(string(docs.ReplayMode)))
	}
	if len(docs.IdempotencyKeyPointers) > 0 {
		parts = append(parts, "idempotencyKeyPointers: "+stringListLiteral(docs.IdempotencyKeyPointers))
	}
	if len(docs.ExpectedRevisionPointers) > 0 {
		parts = append(parts, "expectedRevisionPointers: "+stringListLiteral(docs.ExpectedRevisionPointers))
	}
	if docs.UseWhen != "" {
		parts = append(parts, "useWhen: "+quote(docs.UseWhen))
	}
	if docs.DoNotUseWhen != "" {
		parts = append(parts, "doNotUseWhen: "+quote(docs.DoNotUseWhen))
	}
	if docs.Success != "" {
		parts = append(parts, "success: "+quote(docs.Success))
	}
	if len(docs.Errors) > 0 {
		entries := make([]string, len(docs.Errors))
		for i, docError := range docs.Errors {
			entries[i] = fmt.Sprintf("{ code: %s, description: %s, commonCorrection: %s }",
				quote(docError.Code), quote(docError.Description), quote(docError.CommonCorrection))
		}
		parts = append(parts, "errors: ["+strings.Join(entries, ", ")+"]")
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// operationMCPLiteral renders an @mcp record as the object literal the
// decorator takes: { handle, _meta? } for a visible tool, { hidden: true,
// reason } for a hidden one.
func operationMCPLiteral(mcp *ir.OperationMCP) string {
	if mcp.Hidden {
		return "{ hidden: true, reason: " + quote(mcp.HiddenReason) + " }"
	}
	parts := []string{"handle: " + quote(mcp.Handle)}
	if len(mcp.Meta) > 0 {
		parts = append(parts, "_meta: "+valueLiteral(mcp.Meta))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func stringListLiteral(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = quote(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// valueLiteral renders a decoded JSON value as a TypeScript literal: object
// keys sorted, quoted when they are not identifiers.
func valueLiteral(value any) string {
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, key := range keys {
			name := key
			if !identifierPattern.MatchString(key) {
				name = quote(key)
			}
			parts[i] = name + ": " + valueLiteral(v[key])
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = valueLiteral(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return quote(v)
	case float64:
		return formatFloat(v)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "null"
	default:
		return quote(fmt.Sprint(v))
	}
}
