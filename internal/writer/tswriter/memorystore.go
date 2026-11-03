package tswriter

import (
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// memoryStorePackage declares @channel and @key (D75).
const memoryStorePackage = "@superschematic/memory-store"

// emitMemoryStoreDeclaration renders a MemoryStore schema's channel or key
// as the class the reader reads it from (D75): `@channel({ ... })` or
// `@key({ use, ttlSeconds | maxTtlSeconds, ... })` on an abstract class
// whose fields are the parts. A name the class's kebab case would give is
// left out. A payload, and an enum a part names, is a type the document's
// imports name. The core's reserved declarations are not written: every
// load adds them.
func (e *emitter) emitMemoryStoreDeclaration(decl *ir.MemoryStoreDeclaration) {
	if decl == nil || decl.Reserved {
		return
	}
	pkg := naming.Active().Specifier(memoryStorePackage)
	var parts []string
	decorator := "channel"
	if decl.Kind.IsKey() {
		decorator = "key"
		parts = append(parts, "use: "+quote(string(decl.Kind)))
		if decl.TTLSeconds != 0 {
			parts = append(parts, fmt.Sprintf("ttlSeconds: %d", decl.TTLSeconds))
		}
		if decl.MaxTTLSeconds != 0 {
			parts = append(parts, fmt.Sprintf("maxTtlSeconds: %d", decl.MaxTTLSeconds))
		}
	} else if decl.Payload != nil {
		e.importDocumentSymbol(decl.Payload.Name, fmt.Sprintf("declaration %s: payload", decl.Name))
		parts = append(parts, "payload: "+e.ident(decl.Payload.Name, "payload"))
	}
	if decl.Name != ir.MemoryStoreDeclarationName(decl.Symbol) {
		parts = append(parts, "name: "+quote(decl.Name))
	}
	e.importSymbol(pkg, decorator)
	arg := ""
	if len(parts) > 0 {
		arg = objectLiteral(parts)
	}
	e.body.WriteString("\n")
	e.comment("", decl.Doc)
	fmt.Fprintf(&e.body, "@%s(%s)\n", decorator, arg)
	if len(decl.Parts) == 0 {
		fmt.Fprintf(&e.body, "export abstract class %s {}\n", e.ident(decl.Symbol, "declaration"))
		return
	}
	fmt.Fprintf(&e.body, "export abstract class %s {\n", e.ident(decl.Symbol, "declaration"))
	for _, part := range decl.Parts {
		owner := fmt.Sprintf("%s.%s", decl.Symbol, part.Name)
		if !languagePrimitives[part.Type.Name] && e.doc.Scalars[part.Type.Name] == nil && e.inDocumentImports(part.Type.Name) {
			e.importDocumentSymbol(part.Type.Name, owner)
		}
		fd := &ir.FieldDef{Name: part.Name, TypeRef: part.Type, Required: true}
		fmt.Fprintf(&e.body, "  %s: %s;\n", e.ident(part.Name, "field"), e.fieldTypeExpr(fd, owner, structField))
	}
	e.body.WriteString("}\n")
}

// inDocumentImports reports whether the document's imports name symbol.
func (e *emitter) inDocumentImports(symbol string) bool {
	for _, imp := range e.doc.Imports {
		if slices.Contains(imp.Types, symbol) {
			return true
		}
	}
	return false
}

// importDocumentSymbol imports symbol from the package the document's
// imports name it under, failing when none does.
func (e *emitter) importDocumentSymbol(symbol, owner string) {
	for _, imp := range e.doc.Imports {
		if slices.Contains(imp.Types, symbol) {
			e.importSymbol(imp.Package, symbol)
			return
		}
	}
	e.failf("%s: %s is not covered by the document's imports", owner, symbol)
}
