package tsreader

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The core user model's traits (D50), User and UserRole from
// @superschematic/db, are named in a DB table's implements clause:
//
//	export abstract class Account extends Auditable implements User<{ login: "email" }> { ... }
//	export abstract class Role implements UserRole { ... }
//
// The reader knows them by their import identity and records them as
// TypeDef.User and TypeDef.UserRole. They are the core's, not types of the
// schema, so they are not TraitRefs, they carry no fields to flatten, and
// RawHeritage leaves them out, as an operation set's Authenticated base
// sets a flag and is recorded nowhere else. The schema writer writes them
// back from TypeDef.User and TypeDef.UserRole.
//
// The reader checks the traits' own form and where they stand: the config,
// and that the class is a DB table of a DB schema. Whether the fields they
// name exist and suit them is the verification pass's.

// The user model's trait names, as @superschematic/db declares them.
const (
	userTraitName     = "User"
	userRoleTraitName = "UserRole"
)

// identityTrait returns the name of the user model's trait id names, or
// false when it names another symbol.
func identityTrait(id symbolIdentity) (string, bool) {
	if id.is("@superschematic/db", userTraitName) || id.is("@superschematic/db", userRoleTraitName) {
		return id.name, true
	}
	return "", false
}

// identityTraitEntry resolves one implements-clause entry to the user
// model's trait it names, or false when it names another.
func (w *walker) identityTraitEntry(t *astNode) (string, bool) {
	id, ok := w.identityOf(t.AsExpressionWithTypeArguments().Expression)
	if !ok {
		return "", false
	}
	return identityTrait(id)
}

// applyIdentityTrait records one User or UserRole entry of td's implements
// clause. A malformed entry still marks td, so the placement checks report
// in the same pass; the load fails on the entry's error either way.
func (w *walker) applyIdentityTrait(td *ir.TypeDef, trait string, t *astNode) {
	var typeArgs []*astNode
	if args := t.AsExpressionWithTypeArguments().TypeArguments; args != nil {
		typeArgs = args.Nodes
	}
	switch trait {
	case userTraitName:
		if td.User != nil {
			w.addErr(errorAtNode(t, "%s: the User trait appears more than once", td.Name))
			return
		}
		cfg, serr := w.userTraitConfig(td.Name, t, typeArgs)
		if serr != nil {
			w.addErr(serr)
			cfg = &ir.UserTrait{}
		}
		td.User = cfg
	case userRoleTraitName:
		if td.UserRole != nil {
			w.addErr(errorAtNode(t, "%s: the UserRole trait appears more than once", td.Name))
			return
		}
		if len(typeArgs) > 0 {
			w.addErr(errorAtNode(t, "%s: the UserRole trait takes no type arguments", td.Name))
		}
		td.UserRole = &ir.UserRoleTrait{}
	}
}

// userTraitConfig reads the config of User<{ login: "email"; name:
// "displayName" }>: one type literal whose members are property signatures
// typed by string literals. The compiler checks the config against
// UserConfig, which a key it does not declare still satisfies, so the keys
// are checked here, as relationConfigFromTypeNode checks Relation's.
func (w *walker) userTraitConfig(class string, t *astNode, typeArgs []*astNode) (*ir.UserTrait, *SchemaError) {
	shape := func(at *astNode) *SchemaError {
		return errorAtNode(at, `%s: the User trait's config must be a type literal of string literals, as in User<{ login: "email" }>`, class)
	}
	if len(typeArgs) != 1 {
		return nil, shape(t)
	}
	literal := typeArgs[0]
	if literal.Kind != kindTypeLiteral {
		return nil, shape(literal)
	}
	out := &ir.UserTrait{}
	hasLogin := false
	for _, m := range literal.AsTypeLiteralNode().Members.Nodes {
		if m.Kind != kindPropertySignature {
			return nil, shape(m)
		}
		key, serr := w.evaluatePropertyName(m.Name(), 0)
		if serr != nil {
			return nil, serr
		}
		sig := m.AsPropertySignatureDeclaration()
		value, ok := stringLiteralType(sig.Type)
		if !ok {
			return nil, shape(m)
		}
		if sig.PostfixToken != nil && sig.PostfixToken.Kind == kindQuestionToken {
			return nil, errorAtNode(m, "%s: the User trait's %s must not be optional", class, key)
		}
		if value == "" {
			return nil, errorAtNode(m, "%s: the User trait's %s must name a field", class, key)
		}
		switch key {
		case "login":
			out.Login = value
			hasLogin = true
		case "name":
			out.Name = value
		default:
			return nil, errorAtNode(m, "%s: the User trait's config has unknown key %q; it takes login and name", class, key)
		}
	}
	if !hasLogin {
		return nil, errorAtNode(literal, "%s: the User trait's config needs login, the field a user signs in with", class)
	}
	return out, nil
}

// stringLiteralType returns the text of a string literal type node.
func stringLiteralType(node *astNode) (string, bool) {
	if node == nil || node.Kind != kindLiteralType {
		return "", false
	}
	lit := node.AsLiteralTypeNode().Literal
	if lit.Kind != kindStringLiteral {
		return "", false
	}
	return lit.Text(), true
}

// identityTraits lists the user model's traits td carries.
func identityTraits(td *ir.TypeDef) []string {
	var traits []string
	if td.User != nil {
		traits = append(traits, userTraitName)
	}
	if td.UserRole != nil {
		traits = append(traits, userRoleTraitName)
	}
	return traits
}

// checkIdentityTraitPlacement refuses the user model's traits, at the
// class, on a class that is not a DB table of a DB schema: a class of
// another kind of schema, a type of another role and a @jsonField type,
// and both traits on one table. A base class the schema's other tables
// extend is refused once every file is walked (finishIdentityTraits), so
// the class is recorded for that check.
func (w *walker) checkIdentityTraitPlacement(node *astNode, td *ir.TypeDef) {
	traits := identityTraits(td)
	if len(traits) == 0 {
		return
	}
	if len(traits) > 1 {
		w.addErr(errorAtNode(node, "%s: a table takes the User trait or the UserRole trait, not both", td.Name))
	}
	var why string
	switch {
	case w.cfg.Kind != ir.SchemaKindDB:
		why = fmt.Sprintf("this service is kind %s", w.cfg.Kind)
	case td.Role != ir.RoleDBTable:
		why = fmt.Sprintf("this type has role %s", td.Role)
	case td.JsonField:
		why = "a @jsonField type is stored as JSON and gets no table"
	default:
		w.identityTables[td.Name] = node
		return
	}
	for _, trait := range traits {
		w.addErr(errorAtNode(node, "%s: the %s trait is only allowed on a DB table of a DB schema (%s)", td.Name, trait, why))
	}
}

// finishIdentityTraits refuses the user model's traits on a base class the
// schema's other tables extend. A base class gets no table of its own: its
// fields are copied onto each table that extends it, and the trait is not.
func (w *walker) finishIdentityTraits() {
	if len(w.identityTables) == 0 {
		return
	}
	extenders := make(map[string][]string)
	for _, td := range w.schema.Types {
		if td.Extends != "" {
			extenders[td.Extends] = append(extenders[td.Extends], td.Name)
		}
	}
	names := make([]string, 0, len(w.identityTables))
	for name := range w.identityTables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tables := extenders[name]
		if len(tables) == 0 {
			continue
		}
		sort.Strings(tables)
		verb := "extends"
		if len(tables) > 1 {
			verb = "extend"
		}
		for _, trait := range identityTraits(w.schema.Types[name]) {
			w.addErr(errorAtNode(w.identityTables[name], "%s: the %s trait is only allowed on a DB table of a DB schema (a base class gets no table, and %s %s %s)",
				name, trait, strings.Join(tables, ", "), verb, name))
		}
	}
}
