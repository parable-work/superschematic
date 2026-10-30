package ormgen

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The behaviors of a table whose fields are behaviors, in the Go ORM
// (D103): a DB type that writes `status: Workflow<{ ... }>` stays a table,
// and the fields the behavior owns are columns of it that its row holds and
// the inputs of its create, update and upsert leave out. rules.go embeds
// the database's layout document (behaviorlayout.Of), which the rules Go
// module's adapter (naming.RulesGoModule) reads to run the behaviors' core
// inside the repository's transaction, and each use's operations hang
// under a method of the table's repository named for the use's field,
// db.Order.Status().Transition(ctx, id, params). Nothing here names a
// behavior: the operations, their parameters and results, and the owned
// fields come from each behavior's declaration, narrowed by its config
// where behaviortypes narrows one.

// Rules is what the ORM writes of the schema's tables whose fields are
// behaviors (D103).
type Rules struct {
	// LayoutJSON is behaviorlayout.Of's document, json.MarshalIndent with
	// two spaces, which rules.go embeds.
	LayoutJSON string
}

// RepositoryUse is a field-anchored use of a behavior on a table, as the
// repository writes its operations: under a method named for its field,
// which returns a value of an interface of its own (OrderStatusOperations).
type RepositoryUse struct {
	// ID is the use's id, its anchor field's name (status), and Behavior
	// its registered name (Workflow).
	ID       string
	Behavior string
	// GoName is the repository's method its operations hang under, the
	// use's id in Pascal case (Status).
	GoName string
	// Interface is the operations' interface (OrderStatusOperations), Value
	// the repository's implementation of it (orderStatusOperations) and
	// NoOp the no-op repository's (noOpOrderStatusOperations). Doc is the
	// interface's doc comment.
	Interface string
	Value     string
	NoOp      string
	Doc       string
	// Enums and Structs are the Go types the use's operations name, in the
	// order they are first named: its narrowing's aliases, and each
	// operation's parameters and result and the objects they hold.
	Enums   []GoEnum
	Structs []GoStruct
	// Operations are the use's operations on a row, in the declaration's
	// order.
	Operations []RepositoryOperation
}

// RepositoryOperation is one operation of a use as the repository writes
// it: a method of the use's interface that runs it through the core.
type RepositoryOperation struct {
	// Name is the operation's declared name (transition), GoName the
	// method's (Transition) and Doc its declaration's description.
	Name   string
	GoName string
	Doc    string
	// Params is the Go type of its parameters, and Result of its result:
	// a struct the use declares, a type its narrowing names, a Go builtin,
	// or json.RawMessage for a schema with no Go type.
	Params string
	Result string
	// ResultIsStruct is a result the method returns a pointer to.
	ResultIsStruct bool
}

// MethodDoc is the doc comment of the operation's method: its
// declaration's description after its name, or a sentence that names the
// operation when the declaration gives none.
func (o RepositoryOperation) MethodDoc() string {
	doc := strings.Join(strings.Fields(o.Doc), " ")
	if doc == "" {
		return fmt.Sprintf("%s runs the operation %s on the row id names.", o.GoName, o.Name)
	}
	return o.GoName + " " + lowerFirstWord(doc)
}

// ResultReturn is the operation's result as its method returns it: a
// pointer to a struct, any other type as it is.
func (o RepositoryOperation) ResultReturn() string {
	if o.ResultIsStruct {
		return "*" + o.Result
	}
	return o.Result
}

// GoEnum is a named Go type of strings: an alias a narrowing names, such
// as a Workflow's states, with a constant per value.
type GoEnum struct {
	Name    string
	Doc     string
	Members []GoEnumMember
}

// GoEnumMember is one value of a GoEnum and its constant's name,
// <Enum><behaviortypes.MemberName(value)>, as typegen names a behavior's
// enum values.
type GoEnumMember struct {
	Const string
	Value string
}

// GoStruct is a named Go struct an operation's parameters or result is.
type GoStruct struct {
	Name    string
	Doc     string
	Members []GoStructMember
}

// GoStructMember is one member of a GoStruct: its Go name, its JSON key,
// its Go type, its description, and whether the JSON may leave it out.
type GoStructMember struct {
	Name     string
	JSON     string
	Type     string
	Doc      string
	Optional bool
}

// Tag is the member's struct tag.
func (m GoStructMember) Tag() string {
	if m.Optional {
		return fmt.Sprintf("`json:%q`", m.JSON+",omitempty")
	}
	return fmt.Sprintf("`json:%q`", m.JSON)
}

// RepositoryOwnedField is a field a behavior owns on a table (D103), as an
// upsert fills its row's member from what the core's create gives it.
type RepositoryOwnedField struct {
	// Use is the use's id, Behavior its behavior and Name the field's
	// declared name, by which the core names it in a row's owned fields
	// (enteredAt).
	Use      string
	Behavior string
	Name     string
	// Field is the field's IR name (statusEnteredAt), Member its member of
	// types.<Type> (StatusEnteredAt) and GoType its Go type without the
	// pointer a nullable one adds.
	Field  string
	Member string
	GoType string
	// DateTime is a field the core holds as epoch milliseconds, and
	// Pointer a member that is a pointer.
	DateTime bool
	Pointer  bool
}

// CoreOwnColumn is an own column of a table the core reads and writes, as
// the repository writes the core's value of it: Field is its name in the
// layout, which a create's own fields and an update's patch name it by.
type CoreOwnColumn struct {
	Field string
	// Column is the repository's field of it, nil for a to-one relation,
	// Relationship that relation's.
	Column       *Field
	Relationship *Relationship
}

// CoreValue is the core's value of the column's field held at expr, a Go
// expression of its member's value without the pointer a nullable one
// adds: a date-time as epoch milliseconds, any other value as
// encoding/json writes it.
func (c CoreOwnColumn) CoreValue(expr string) string {
	if c.Column != nil && c.Column.IsDateTimeScalar {
		return "time.Time(" + expr + ").UnixMilli()"
	}
	return expr
}

// Present is the condition under which variable's member holds the
// column's field, variable a *types.<Type>: a member that is nil when
// unset is present when it is not, a Default<T, "now"> one when it is not
// zero, as the insert writes them; "" for a member always present. A
// to-one relation is the template's.
func (c CoreOwnColumn) Present(variable string) string {
	if c.Column == nil {
		return ""
	}
	f := c.Column
	member := variable + "." + codegen.ToPascalCase(f.Name)
	switch {
	case f.GoPointer() || f.IsArray || f.IsMap || f.OptionalNilCheck:
		return member + " != nil"
	case f.DefaultsToNow && f.IsDateTimeScalar:
		return "!time.Time(" + member + ").IsZero()"
	}
	return ""
}

// Value is the core's value of the column's field in variable, a
// *types.<Type> whose member holds it (Present): read through the pointer
// a nullable member is.
func (c CoreOwnColumn) Value(variable string) string {
	f := c.Column
	member := variable + "." + codegen.ToPascalCase(f.Name)
	if f.GoPointer() && !f.IsArray && !f.IsMap {
		member = "*" + member
	}
	return c.CoreValue(member)
}

// FieldAt is a field with the variable whose member holds it, which a
// template that writes the field's value reads it from.
type FieldAt struct {
	Field Field
	Var   string
}

// At is the field held by variable's member.
func (f Field) At(variable string) FieldAt {
	return FieldAt{Field: f, Var: variable}
}

// Member is the Go expression of the field's member of the variable.
func (a FieldAt) Member() string {
	return a.Var + "." + codegen.ToPascalCase(a.Field.Name)
}

// MemberGoType is the Go type typegen gives the field's member of
// types.<Type>: ValueGoType, behind a pointer when the member is one.
func (f Field) MemberGoType() string {
	value := f.ValueGoType()
	if f.GoPointer() && !f.IsArray && !f.IsMap {
		return "*" + value
	}
	return value
}

// MemberGoType is the Go type typegen gives a to-one relationship's member
// of types.<Type>: its target, behind a pointer when it is optional.
func (r Relationship) MemberGoType() string {
	if r.IsRequired {
		return "types." + r.TargetType
	}
	return "*types." + r.TargetType
}

// CreateRelationships are the to-one relationships <Type>Create holds,
// whose keys a create writes.
func (r Repository) CreateRelationships() []Relationship {
	var out []Relationship
	for _, rel := range r.Relationships {
		if !rel.IsArray {
			out = append(out, rel)
		}
	}
	return out
}

// CreateInput is the input of the repository's creates and upserts: the
// row itself, or <Type>Create for a table whose fields are behaviors
// (D103).
func (r Repository) CreateInput() string {
	if r.HasBehaviors {
		return r.TypeName + "Create"
	}
	return "types." + r.TypeName
}

// HasIfVersion reports whether the repository has UpdateOneIfVersion and
// DeleteOneIfVersion: a table with a _version column whose fields are not
// behaviors. The version of a table whose fields are behaviors is the
// fence every write through the core holds (D103).
func (r Repository) HasIfVersion() bool {
	return r.HasVersion && !r.HasBehaviors
}

// FieldColumn is the column of the field named name, unquoted, or "" when
// the table has no such field.
func (r Repository) FieldColumn(name string) string {
	for _, f := range r.Fields {
		if f.Name == name {
			return f.DBName
		}
	}
	return ""
}

// HasField reports whether the table has the field named name.
func (r Repository) HasField(name string) bool {
	return r.FieldColumn(name) != ""
}

// WireKey is the key the core names the row id holds by, id a Go
// expression of the primary key's type: a UUID's base62 text, any other
// key's text.
func (r Repository) WireKey(id string) string {
	if r.PrimaryKeyIsUUID {
		return id + ".String()"
	}
	return "fmt.Sprint(" + id + ")"
}

// KeyParse is the function that reads a UUID key from its wire form,
// types.Parse<Scalar>, for a table whose key is a UUID.
func (r Repository) KeyParse() string {
	return "types.Parse" + strings.TrimPrefix(r.PrimaryKeyType, "types.")
}

// PrimaryKey is the repository's primary key field.
func (r Repository) PrimaryKey() Field {
	for _, f := range r.Fields {
		if f.IsPrimaryKey {
			return f
		}
	}
	return Field{}
}

// OperationsUseRawJSON reports whether an operation of a use of any
// table's takes or returns json.RawMessage, which interfaces.go's no-op
// operations name.
func (o *ORMOutput) OperationsUseRawJSON() bool {
	for _, repo := range o.Repositories {
		for _, use := range repo.Uses {
			for _, op := range use.Operations {
				if strings.Contains(op.Params, "json.RawMessage") || strings.Contains(op.ResultReturn(), "json.RawMessage") {
					return true
				}
			}
		}
	}
	return false
}

// RulesExample is a call of the first operation of the first use of the
// first table whose fields are behaviors, as the README shows one:
// db.Order.Status().Transition(ctx, id, params).
func (o *ORMOutput) RulesExample() string {
	for _, repo := range o.Repositories {
		for _, use := range repo.Uses {
			for _, op := range use.Operations {
				return fmt.Sprintf("db.%s.%s().%s(ctx, id, params)", repo.TypeName, use.GoName, op.GoName)
			}
		}
	}
	return ""
}

// repositoryMethods are the exported methods a repository holds itself,
// which a use's method may not take; ListAsOfBy* is checked by its prefix.
var repositoryMethods = []string{
	"GetOne", "GetManyByIDs", "FindOne", "FindMany", "FindPage",
	"CreateOne", "CreateMany", "UpsertOne", "UpsertOneResult", "UpsertMany",
	"UpdateOne", "UpdateOneIfVersion", "UpdateMany", "DeleteOne", "DeleteOneIfVersion",
	"DeleteMany", "HardDeleteOne", "GetVersion", "ListVersions", "GetAsOf",
	"PruneHistory", "PruneHistoryFunctionName", "Nearest", "Reencrypt",
}

// repositoryTypeSuffixes are the suffixes of the names the ORM declares for
// each table, after its type's name; repositoryTypePrefixes the prefixes
// before it.
var (
	repositoryTypeSuffixes = []string{
		"Repository", "RepositoryInterface", "Filter", "Update", "Fields", "OrderBy", "OrderByField",
		"GetOptions", "FindOptions", "PageOptions", "Page", "PageKey", "SortField", "Sort", "UniqueKey",
		"AllowedFields", "QuotedFields", "HistoryOptions", "VectorField", "NearestOptions", "Neighbour",
		"FilterConditions", "Create",
	}
	repositoryTypePrefixes = []string{"NoOp", "New", "Parse"}
)

// packageNames are the names the ORM package declares whatever its schema:
// database.go's, transaction.go's, upsert.go's and rules.go's exported
// declarations.
var packageNames = []string{
	"Database", "Tx", "TxInterface", "DatabaseInterface", "NoOpDatabase", "NoOpTx", "TxOptions",
	"IsolationLevel", "UpsertOptions", "UpsertResult", "UpsertStatus", "UpsertInserted", "UpsertUpdated",
	"UpsertUnchanged", "ErrNotFound", "ErrVersionConflict", "ErrNoUserInContext", "RulesLayout",
	"WithBehaviorCaller", "WithUserID", "GetUserID", "HasUserID", "WithCaller", "ParseUserID",
	"Connect", "ConnectWithPool",
}

// attachRules gives the repositories of the schema's tables whose fields
// are behaviors what they write of them (D103), and returns the layout
// document rules.go embeds; nil for a schema with no such table. It fails
// for a table whose behaviors no registry declares, and for a use whose
// method or a type its operations name would take a name the package
// already holds.
func attachRules(schema *ir.Schema, repositories []Repository, reg *registry.Registry) (*Rules, error) {
	var tables []*ir.TypeDef
	for _, repo := range repositories {
		if td := schema.Types[repo.TypeName]; td != nil && len(td.FieldBehaviors()) > 0 {
			tables = append(tables, td)
		}
	}
	if len(tables) == 0 {
		return nil, nil
	}
	if reg == nil {
		return nil, fmt.Errorf("ormgen: table %s composes behaviors, and no registry declares them", tables[0].Name)
	}
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil {
		return nil, fmt.Errorf("ormgen: %w", err)
	}
	if layout == nil {
		return nil, nil
	}

	// The names the package declares, by what declares them: each table's,
	// then each use's as it adds them.
	names := map[string]string{}
	for _, name := range packageNames {
		names[name] = "the ORM package"
	}
	for _, repo := range repositories {
		for _, suffix := range repositoryTypeSuffixes {
			if suffix == "Create" && schema.Types[repo.TypeName] != nil && len(schema.Types[repo.TypeName].FieldBehaviors()) == 0 {
				continue
			}
			names[repo.TypeName+suffix] = "the repository of " + repo.TypeName
		}
		for _, prefix := range repositoryTypePrefixes {
			names[prefix+repo.TypeName+"Repository"] = "the repository of " + repo.TypeName
		}
		names["New"+repo.TypeName+"SnapshotUpdate"] = "the repository of " + repo.TypeName
		names["Parse"+repo.TypeName+"Sort"] = "the repository of " + repo.TypeName
	}

	for i := range repositories {
		repo := &repositories[i]
		table := layout.Types[repo.TypeName]
		if table == nil {
			continue
		}
		if err := attachTableRules(schema, repo, table, reg, names); err != nil {
			return nil, err
		}
	}

	encoded, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("ormgen: the behaviors' layout: %w", err)
	}
	if strings.Contains(string(encoded), "`") {
		return nil, fmt.Errorf("ormgen: the behaviors' layout holds a backquote, which rules.go's raw string cannot: %s", encoded)
	}
	return &Rules{LayoutJSON: string(encoded)}, nil
}

// attachTableRules gives repo, a table whose fields are behaviors, its
// uses, its owned fields and its own columns as the core reads them.
// names holds the names the package declares, which each name a use adds
// joins.
func attachTableRules(schema *ir.Schema, repo *Repository, table *behaviorlayout.Table, reg *registry.Registry, names map[string]string) error {
	td := schema.Types[repo.TypeName]
	repo.HasBehaviors = true
	repo.NeedsTime = true

	for _, column := range table.Own {
		own := CoreOwnColumn{Field: column.Field}
		for i := range repo.Fields {
			if repo.Fields[i].DBName == column.Column {
				own.Column = &repo.Fields[i]
			}
		}
		for i := range repo.Relationships {
			if !repo.Relationships[i].IsArray && repo.Relationships[i].DBColumnName == column.Column {
				own.Relationship = &repo.Relationships[i]
			}
		}
		switch {
		case own.Column == nil && own.Relationship == nil:
			return fmt.Errorf("ormgen: table %s: the behaviors' layout names the own column %s, which the repository does not hold", repo.TypeName, column.Column)
		case own.Column != nil && own.Column.IsEncrypted:
			// An encrypted field's envelope is no value the core reads.
			continue
		case own.Column != nil && (own.Column.IsArray || own.Column.IsMap) && behaviorlayout.ConvertOf(column.Type) == behaviorlayout.ConvertEpochMillis:
			// No adapter converts a list or a map of date-times, which the
			// layout leaves unconverted: the core is given none of it, as of
			// an encrypted field.
			continue
		case own.Column != nil && (column.Convert == behaviorlayout.ConvertEpochMillis) != own.Column.IsDateTimeScalar:
			return fmt.Errorf("ormgen: table %s: the behaviors' layout converts %s as %q, and the repository holds it as a date-time %t", repo.TypeName, column.Field, column.Convert, own.Column.IsDateTimeScalar)
		}
		repo.CoreOwn = append(repo.CoreOwn, own)
	}

	for _, use := range table.Uses {
		goName := codegen.ToPascalCase(use.ID)
		if slices.Contains(repositoryMethods, goName) || strings.HasPrefix(goName, "ListAsOfBy") {
			return fmt.Errorf("ormgen: table %s: the behavior %s on field %s would put its operations under the repository's method %s, which the repository holds itself; rename the field", repo.TypeName, use.Behavior, use.ID, goName)
		}
		for _, field := range use.Fields {
			var fd *Field
			for i := range repo.Fields {
				if repo.Fields[i].DBName == field.Column.Column {
					fd = &repo.Fields[i]
				}
			}
			if fd == nil {
				return fmt.Errorf("ormgen: table %s: field %s: behavior %s's field %s is the column %s, which the repository does not hold", repo.TypeName, use.ID, use.Behavior, field.Name, field.Column.Column)
			}
			dateTime := field.Convert == behaviorlayout.ConvertEpochMillis
			if dateTime != fd.IsDateTimeScalar {
				return fmt.Errorf("ormgen: table %s: field %s: the behaviors' layout converts %s as %q, and the repository holds it as a date-time %t", repo.TypeName, use.ID, field.Field, field.Convert, fd.IsDateTimeScalar)
			}
			repo.OwnedFields = append(repo.OwnedFields, RepositoryOwnedField{
				Use:      use.ID,
				Behavior: use.Behavior,
				Name:     field.Name,
				Field:    fd.Name,
				Member:   codegen.ToPascalCase(fd.Name),
				GoType:   fd.ValueGoType(),
				DateTime: dateTime,
				Pointer:  fd.MemberGoType() != fd.ValueGoType(),
			})
		}

		ref, _ := td.FieldBehavior(use.ID)
		declaration, ok := reg.Behavior(use.Behavior)
		if !ok {
			return fmt.Errorf("ormgen: table %s: field %s: behavior %s is not registered", repo.TypeName, use.ID, use.Behavior)
		}
		base := repo.TypeName + goName
		narrowed, err := behaviortypes.Narrow(declaration, ref.Config, behaviortypes.Target{
			Schema:   schema.Name,
			Base:     base,
			Document: schema,
			Instance: td,
		})
		if err != nil {
			return fmt.Errorf("ormgen: table %s: field %s: behavior %s: %w", repo.TypeName, use.ID, use.Behavior, err)
		}
		// A narrowing's doc names the type it narrows by its base, which for
		// a use on a field is the type's name and the field's (an
		// OrderStatus's): its docs name the field instead (Order's status).
		aliases := make([]behaviortypes.Alias, len(narrowed.Aliases))
		for i, alias := range narrowed.Aliases {
			alias.Doc = strings.ReplaceAll(alias.Doc, behaviortypes.An(base)+"'s", repo.TypeName+"'s "+use.ID)
			aliases[i] = alias
		}
		b := newGoTypeBuilder(aliases)
		ru := RepositoryUse{
			ID:        use.ID,
			Behavior:  use.Behavior,
			GoName:    goName,
			Interface: base + "Operations",
			Value:     lowerFirstRune(base) + "Operations",
			NoOp:      "noOp" + base + "Operations",
			Doc: fmt.Sprintf("%sOperations are the operations of %s's %s, %s (D103), each run through the behaviors' core on the row whose key it takes, in a transaction.",
				base, repo.TypeName, use.ID, behaviortypes.An(use.Behavior)),
		}
		for _, op := range use.Operations {
			var doc string
			for _, declared := range declaration.Operations {
				if declared.Name == op.Name {
					doc = declared.Description
				}
			}
			opGo := codegen.ToPascalCase(op.Name)
			params, _, err := b.typeOf(narrowed.Params[op.Name], base+opGo+"Params",
				fmt.Sprintf("%s are the parameters of %s's %s.", base+opGo+"Params", use.ID, op.Name))
			if err != nil {
				return fmt.Errorf("ormgen: table %s: field %s: operation %s's parameters: %w", repo.TypeName, use.ID, op.Name, err)
			}
			result, isStruct, err := b.typeOf(narrowed.Results[op.Name], base+opGo+"Result",
				fmt.Sprintf("%s is what %s's %s returns.", base+opGo+"Result", use.ID, op.Name))
			if err != nil {
				return fmt.Errorf("ormgen: table %s: field %s: operation %s's result: %w", repo.TypeName, use.ID, op.Name, err)
			}
			ru.Operations = append(ru.Operations, RepositoryOperation{
				Name:           op.Name,
				GoName:         opGo,
				Doc:            doc,
				Params:         params,
				Result:         result,
				ResultIsStruct: isStruct,
			})
		}
		ru.Enums, ru.Structs = b.enums, b.structs

		added := []string{ru.Interface}
		for _, e := range ru.Enums {
			added = append(added, e.Name)
			for _, m := range e.Members {
				added = append(added, m.Const)
			}
		}
		for _, s := range ru.Structs {
			added = append(added, s.Name)
		}
		for _, name := range added {
			if owner, taken := names[name]; taken {
				return fmt.Errorf("ormgen: table %s: the behavior %s on field %s would name %s, which %s already names; rename the field", repo.TypeName, use.Behavior, use.ID, name, owner)
			}
			names[name] = fmt.Sprintf("the behavior %s on %s.%s", use.Behavior, repo.TypeName, use.ID)
		}
		repo.Uses = append(repo.Uses, ru)
	}
	return nil
}

// goTypeBuilder writes the Go types of a use's narrowed schemas: each
// alias the narrowing names, once, and each object as a struct named for
// the path to it.
type goTypeBuilder struct {
	aliases map[string]behaviortypes.Alias
	order   []string
	enums   []GoEnum
	structs []GoStruct
	named   map[string]bool
}

func newGoTypeBuilder(aliases []behaviortypes.Alias) *goTypeBuilder {
	b := &goTypeBuilder{aliases: map[string]behaviortypes.Alias{}, named: map[string]bool{}}
	for _, alias := range aliases {
		b.aliases[alias.Name] = alias
		b.order = append(b.order, alias.Name)
	}
	return b
}

// typeOf is the Go type of a narrowed schema: an alias its named type; an
// object of named members a struct called name, whose doc is doc; a
// string, an integer, a number and a boolean Go's (a listed enum no alias
// names stays a string); an array a slice; a nullable scalar or struct a
// pointer; and anything else, a union, an object of no named members or
// no schema at all, json.RawMessage. isStruct reports a struct name.
func (b *goTypeBuilder) typeOf(schema any, name, doc string) (goType string, isStruct bool, err error) {
	s, ok := schema.(map[string]any)
	if !ok {
		return "json.RawMessage", false, nil
	}
	if ref, ok := s[behaviortypes.RefKey].(string); ok {
		nullable, _ := s["nullable"].(bool)
		named, isStruct, err := b.alias(ref)
		if err != nil {
			return "", false, err
		}
		if nullable && named != "json.RawMessage" {
			return "*" + named, false, nil
		}
		return named, isStruct, nil
	}
	if _, ok := s["const"]; ok {
		return "json.RawMessage", false, nil
	}
	for _, union := range []string{"anyOf", "oneOf", "allOf", "not"} {
		if _, ok := s[union]; ok {
			return "json.RawMessage", false, nil
		}
	}
	nullable, kinds := schemaKinds(s)
	if _, ok := s["enum"]; ok {
		if len(kinds) != 1 || kinds[0] != "string" {
			return "json.RawMessage", false, nil
		}
	}
	if len(kinds) == 0 {
		if _, ok := s["properties"]; !ok {
			return "json.RawMessage", false, nil
		}
		kinds = []string{"object"}
	}
	if len(kinds) > 1 {
		return "json.RawMessage", false, nil
	}
	pointer := func(t string) string {
		if nullable {
			return "*" + t
		}
		return t
	}
	switch kinds[0] {
	case "string":
		return pointer("string"), false, nil
	case "integer":
		return pointer("int64"), false, nil
	case "number":
		return pointer("float64"), false, nil
	case "boolean":
		return pointer("bool"), false, nil
	case "array":
		var items any = true
		if declared, ok := s["items"]; ok {
			items = declared
		}
		elem, _, err := b.typeOf(items, name+"Item", fmt.Sprintf("%s types an item of %s.", name+"Item", name))
		if err != nil {
			return "", false, err
		}
		return "[]" + elem, false, nil
	case "object":
		properties, _ := s["properties"].(map[string]any)
		if len(properties) == 0 {
			return "json.RawMessage", false, nil
		}
		if err := b.object(s, properties, name, doc); err != nil {
			return "", false, err
		}
		if nullable {
			return "*" + name, false, nil
		}
		return name, true, nil
	}
	return "json.RawMessage", false, nil
}

// object adds the struct name an object schema is, its members in the
// order of their keys: a required member by value, an optional one a
// pointer, or a slice or json.RawMessage, left out of the JSON when unset.
func (b *goTypeBuilder) object(s, properties map[string]any, name, doc string) error {
	if err := b.claim(name); err != nil {
		return err
	}
	index := len(b.structs)
	b.structs = append(b.structs, GoStruct{Name: name, Doc: doc})
	required := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, each := range list {
			if text, ok := each.(string); ok {
				required[text] = true
			}
		}
	}
	var members []GoStructMember
	seen := map[string]string{}
	for _, property := range behaviortypes.SortedKeys(properties) {
		member := behaviortypes.MemberName(property)
		if prev, taken := seen[member]; taken {
			return fmt.Errorf("%s and %s would both be the member %s of %s", prev, property, member, name)
		}
		seen[member] = property
		path := name + member
		goType, _, err := b.typeOf(behaviortypes.WithoutDescription(properties[property]), path, fmt.Sprintf("%s types %s.%s.", path, name, property))
		if err != nil {
			return err
		}
		optional := !required[property]
		if optional && !strings.HasPrefix(goType, "*") && !strings.HasPrefix(goType, "[]") && goType != "json.RawMessage" {
			goType = "*" + goType
		}
		members = append(members, GoStructMember{
			Name:     member,
			JSON:     property,
			Type:     goType,
			Doc:      memberDoc(member, behaviortypes.DescriptionOf(properties[property])),
			Optional: optional,
		})
	}
	b.structs[index].Members = members
	return nil
}

// alias adds the type a narrowing names, once: an enum of strings with a
// constant per value, a struct for an object of named members, and for
// anything else no type, json.RawMessage in its place.
func (b *goTypeBuilder) alias(name string) (string, bool, error) {
	alias, ok := b.aliases[name]
	if !ok {
		// A type of the narrowing's own (<Base>Fields, <Base>Patch) that no
		// alias declares.
		return "json.RawMessage", false, nil
	}
	if slices.ContainsFunc(b.enums, func(e GoEnum) bool { return e.Name == name }) {
		return name, false, nil
	}
	if slices.ContainsFunc(b.structs, func(s GoStruct) bool { return s.Name == name }) {
		return name, true, nil
	}
	node, _ := alias.Schema.(map[string]any)
	if values, ok := node["enum"].([]any); ok {
		if err := b.claim(name); err != nil {
			return "", false, err
		}
		enum := GoEnum{Name: name, Doc: name + " is " + lowerFirstWord(alias.Doc)}
		seen := map[string]string{}
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return "", false, fmt.Errorf("its schema refers to %s, which is not an enum of strings", name)
			}
			constName := name + behaviortypes.MemberName(text)
			if prev, taken := seen[constName]; taken {
				return "", false, fmt.Errorf("the values %s and %s of %s would both be the constant %s", prev, text, name, constName)
			}
			seen[constName] = text
			enum.Members = append(enum.Members, GoEnumMember{Const: constName, Value: text})
		}
		b.enums = append(b.enums, enum)
		return name, false, nil
	}
	if properties, ok := node["properties"].(map[string]any); ok && len(properties) > 0 {
		doc := name + " is " + lowerFirstWord(alias.Doc)
		if err := b.object(node, properties, name, doc); err != nil {
			return "", false, err
		}
		return name, true, nil
	}
	return "json.RawMessage", false, nil
}

// claim records a type name the use declares, refusing one it already has.
func (b *goTypeBuilder) claim(name string) error {
	if b.named[name] {
		return fmt.Errorf("two of its types would be named %s", name)
	}
	b.named[name] = true
	return nil
}

// schemaKinds reads a schema's type: whether it takes null, and its other
// types.
func schemaKinds(s map[string]any) (nullable bool, kinds []string) {
	switch t := s["type"].(type) {
	case string:
		kinds = []string{t}
	case []any:
		for _, each := range t {
			if name, ok := each.(string); ok {
				kinds = append(kinds, name)
			}
		}
	}
	if i := slices.Index(kinds, "null"); i >= 0 {
		return true, slices.Delete(kinds, i, i+1)
	}
	if nullable, ok := s["nullable"].(bool); ok && nullable {
		return true, kinds
	}
	return false, kinds
}

// memberDoc is a member's doc comment: "<Member> is <description>", or ""
// for a member with no description.
func memberDoc(member, description string) string {
	description = strings.Join(strings.Fields(description), " ")
	if description == "" {
		return ""
	}
	return member + " is " + lowerFirstWord(description)
}

// lowerFirstWord lowers the first letter of a sentence whose first word is
// not an acronym (URL, ID), which reads on after a name.
func lowerFirstWord(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	if size == 0 || !unicode.IsUpper(first) {
		return text
	}
	if next, _ := utf8.DecodeRuneInString(text[size:]); unicode.IsUpper(next) {
		return text
	}
	return string(unicode.ToLower(first)) + text[size:]
}

// docComment is text as a doc comment at indent, wrapped before the 78th
// column: each line "// " and words.
func docComment(indent, text string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	const width = 78
	var lines []string
	line := indent + "//"
	for _, word := range words {
		if len(line)+1+len(word) > width && line != indent+"//" {
			lines = append(lines, line)
			line = indent + "//"
		}
		line += " " + word
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n")
}
