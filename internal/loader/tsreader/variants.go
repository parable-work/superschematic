package tsreader

import (
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// variantUsage is how a variant union is written, for its errors.
const variantUsage = `a variant union is an exported alias of Variant<By, Arms>: export type StepResult = Variant<"kind", { verify: VerifyResult; review: ReviewResult }>, By the sibling field that picks the member and Arms a member type for each of its values`

// variantAlias reports whether node, a type alias's type, is the authoring
// package's Variant<By, Arms>.
func (w *walker) variantAlias(node *astNode) bool {
	if node == nil || node.Kind != kindTypeReference {
		return false
	}
	id, ok := w.identityOf(node.AsTypeReferenceNode().TypeName)
	return ok && w.isAuthoring(id) && id.name == "Variant"
}

// walkVariantAlias reads `export type StepResult = Variant<"kind", { verify:
// VerifyResult; review: ReviewResult }>` into a variant union (D149): a
// union whose member a sibling field names by value. Types are the arms'
// members in the order the arms first name them. Each member is an object
// type the service declares, as every union member is.
func (w *walker) walkVariantAlias(node *astNode) {
	name := node.Name().Text()
	ref := node.AsTypeAliasDeclaration().Type.AsTypeReferenceNode()
	if ref.TypeArguments == nil || len(ref.TypeArguments.Nodes) != 2 {
		w.addErr(errorAtNode(node, "%s: %s", name, variantUsage))
		return
	}
	args := ref.TypeArguments.Nodes
	by, ok := stringLiteralType(args[0])
	if !ok || by == "" {
		w.addErr(errorAtNode(args[0], "%s: Variant's By is a string literal, the name of the sibling field that picks the member; %s", name, variantUsage))
		return
	}
	arms := args[1]
	for arms.Kind == kindParenthesizedType {
		arms = arms.AsParenthesizedTypeNode().Type
	}
	if arms.Kind != kindTypeLiteral {
		w.addErr(errorAtNode(args[1], "%s: Variant's Arms is an object type written in place, a member type for each value of %s; %s", name, by, variantUsage))
		return
	}
	def := &ir.UnionDef{Name: name, Comment: w.typeComment(node), By: by, Arms: map[string]string{}}
	for _, m := range arms.AsTypeLiteralNode().Members.Nodes {
		if m.Kind != kindPropertySignature {
			w.addErr(errorAtNode(m, "%s: Variant's Arms holds only properties, a value of %s and its member type", name, by))
			continue
		}
		sig := m.AsPropertySignatureDeclaration()
		value := m.Name().Text()
		if sig.PostfixToken != nil && sig.PostfixToken.Kind == kindQuestionToken {
			w.addErr(errorAtNode(m, "%s: arm %q is optional; every arm names its member", name, value))
			continue
		}
		if sig.Type == nil {
			w.addErr(errorAtNode(m, "%s: arm %q names no member type", name, value))
			continue
		}
		info, serr := w.resolveTypeNode(sig.Type)
		if serr != nil {
			w.addErr(serr)
			continue
		}
		if !info.isObject || info.ref.IsArray || info.ref.IsMap || info.nullable || info.importPkg != "" {
			w.addErr(errorAtNode(sig.Type, "%s: arm %q's member is an object type this service declares, not a list, a map, null or another service's type", name, value))
			continue
		}
		if _, dup := def.Arms[value]; dup {
			w.addErr(errorAtNode(m, "%s: arm %q is written twice", name, value))
			continue
		}
		def.Arms[value] = info.ref.Name
		if !slices.Contains(def.Types, info.ref.Name) {
			def.Types = append(def.Types, info.ref.Name)
		}
	}
	if len(def.Arms) == 0 {
		w.addErr(errorAtNode(args[1], "%s: Variant's Arms names no member; %s", name, variantUsage))
		return
	}
	if w.schema.Unions[name] != nil || w.schema.Types[name] != nil || w.schema.Enums[name] != nil {
		w.addErr(errorAtNode(node, "duplicate type %q", name))
		return
	}
	w.schema.Unions[name] = def
}
