package tswriter

import (
	"fmt"
	"reflect"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

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
