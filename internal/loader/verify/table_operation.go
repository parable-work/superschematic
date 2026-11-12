package verify

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkTableOperations holds each @tableOperation (D106) to its table and
// fills what the decorator leaves unsaid, in every form. The operation is
// an ordinary declaration: its route, guards, arguments and result are
// what it writes. What this pass decides is whether a handler the build
// writes can serve it:
//
//   - the API has a database (ir.Schema.Database), and the method's views,
//     its result and its body, are the API's own @source views of one
//     table of it; a read and a list may read a projection with a key in
//     the table's place (D90);
//   - the HTTP method and shape name one of the five kinds
//     (ir.TableOperationKinds), and the message says what was found when
//     they name none; a PUT is refused;
//   - the route's path parameters are the table's key and the keys of its
//     to-one relations, which hold the row to its parents; every other
//     argument is the body, a list's SortBy or its SearchQuery;
//   - nothing is served that a view does not list: a view with a @virtual
//     field has no implementation to fill it;
//   - a create's body sets only columns a caller may set and every column
//     the table requires; an update's members are all optional;
//   - a list is @paginated, sorts by columns the repositories sort by, and
//     searches a table with search fields;
//   - no guard asks an implementation to run: @requireOwnership and
//     @manualRouteRegistration fail the load;
//   - a row rule (D108) names members of the row that hold a subject, on a
//     route that requires a caller, and a create's body carries none of
//     the members the rule sets to the caller.
//
// It writes TableOperation's Kind, Database, Type, Projection, Key,
// Parents and Input, and keeps the row rule the decorator wrote, By and
// OverridePermission. A data form that wrote one of the first otherwise
// is refused, since what an operation does is its declaration's to say.
func checkTableOperations(schema *ir.Schema, in Input, r *Result) {
	type marked struct {
		set *ir.OperationSet
		op  *ir.FieldDef
	}
	var ops []marked
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op != nil && op.TableOperation != nil {
				ops = append(ops, marked{set, op})
			}
		}
	}
	if len(ops) == 0 {
		return
	}
	first := ops[0].set.Name + "." + ops[0].op.Name
	if kind, _ := in.registry().Kind(string(schema.Kind)); schema.Kind != ir.SchemaKindAPI || kind.SourceProjectionRole == "" {
		r.errorf("", "%s: @tableOperation serves a table of an API's database, and %s is a %s schema", first, schema.Name, schema.Kind)
		return
	}
	dbName, _, ok := schema.Database()
	if !ok {
		deps := 0
		for _, dep := range schema.Dependencies {
			if dep.Kind == ir.SchemaKindDB {
				deps++
			}
		}
		if deps > 1 {
			r.errorf("", "%s: @tableOperation reads and writes a table of the API's database, and %s names %d DB dependencies and no authDb: set its authDb to the one that declares the table", first, schema.Name, deps)
		} else {
			r.errorf("", "%s: @tableOperation reads and writes a table of the API's database, and %s connects to none: set its authDb, or give it one DB dependency, the DB service that declares the table", first, schema.Name)
		}
		return
	}
	if in.Database == nil {
		r.errorf("", "%s: @tableOperation reads and writes a table of %s, the API's database, which the loader could not read", first, dbName)
		return
	}
	db := &tableDatabase{name: dbName, schema: in.Database}
	if err := db.model(); err != nil {
		r.errorf("", "%s: reading the tables of %s, the API's database: %v", first, dbName, err)
		return
	}
	for _, m := range ops {
		checkTableOperation(schema, db, m.set, m.op, r)
	}
}

// CheckTableOperations is the @tableOperation part of Run on its own, for
// a caller that needs only its findings.
func CheckTableOperations(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkTableOperations(schema, in, r)
	return r
}

// tableDatabase is the API's database as the table operations read it:
// its tables and its keyed projections, modelled as the ORMs model them.
type tableDatabase struct {
	name   string
	schema *ir.Schema
	tables map[string]*sqlutil.TableModel
	views  map[string]*sqlutil.TableModel
}

func (d *tableDatabase) model() error {
	tables, err := sqlutil.TablesOf(d.schema, nil)
	if err != nil {
		return err
	}
	views, err := sqlutil.ViewsOf(d.schema, nil)
	if err != nil {
		return err
	}
	d.tables = make(map[string]*sqlutil.TableModel, len(tables))
	for i := range tables {
		d.tables[tables[i].Type] = &tables[i]
	}
	d.views = make(map[string]*sqlutil.TableModel, len(views))
	for i := range views {
		d.views[views[i].Type] = &views[i]
	}
	return nil
}

// pathParameter matches one {name} of a route's path.
var pathParameter = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// tableView is a view an operation names, held to its table: the view,
// the table or projection it is @source of, and that one's model.
type tableView struct {
	view       *ir.TypeDef
	source     *ir.TypeDef
	model      *sqlutil.TableModel
	projection bool
}

// column finds the column a view's field reads or sets: the column of the
// field's own name, or a to-one relation's, for its key (assigneeId).
func (v *tableView) column(field string) *sqlutil.ColumnModel {
	for i := range v.model.Columns {
		c := &v.model.Columns[i]
		if (c.Relation == "" && c.Field == field) || (c.Relation != "" && c.Field+"Id" == field) {
			return c
		}
	}
	return nil
}

// viewOf holds a type an operation names to a table of the database: one
// of the API's own types, @source of a table, or of a projection with a
// key. what says which view it is, for the message: "result" or "body".
func viewOf(schema *ir.Schema, db *tableDatabase, owner, what, typeName string, r *Result) (*tableView, bool) {
	td := schema.Types[typeName]
	if td == nil {
		td = schema.Inputs[typeName]
	}
	if td == nil {
		r.errorf("", "%s: its %s is %s, which is no type of %s; @tableOperation serves @source views of a table of %s", owner, what, typeName, schema.Name, db.name)
		return nil, false
	}
	if td.Source == nil {
		r.errorf("", "%s: its %s %s is a plain type; @tableOperation reads and writes @source views of a table of %s, and writes no handler for anything else", owner, what, td.Name, db.name)
		return nil, false
	}
	service, name := splitTarget(td.Source.Target)
	if service != db.name {
		from := service
		if from == "" {
			from = schema.Name
		}
		r.errorf("", "%s: its %s %s is @source(%s), a type of %s, not of %s, the API's database (its authDb, or its one DB dependency)", owner, what, td.Name, name, from, db.name)
		return nil, false
	}
	source := db.schema.Types[name]
	if source == nil {
		r.errorf("", "%s: its %s %s is @source(%s), which %s does not declare", owner, what, td.Name, name, db.name)
		return nil, false
	}
	view := &tableView{view: td, source: source}
	switch {
	case source.Projection != nil:
		view.projection = true
		view.model = db.views[name]
		if view.model == nil {
			r.errorf("", "%s: its %s %s is @source(%s), a projection with no key: it reads neither its source's key nor one column it collapses by, so it has no repository to read it through", owner, what, td.Name, name)
			return nil, false
		}
	default:
		view.model = db.tables[name]
		if view.model == nil {
			r.errorf("", "%s: its %s %s is @source(%s), which is no table of %s with a repository", owner, what, td.Name, name, db.name)
			return nil, false
		}
	}
	return view, true
}

// checkTableOperation is checkTableOperations for one operation.
func checkTableOperation(schema *ir.Schema, db *tableDatabase, set *ir.OperationSet, op *ir.FieldDef, r *Result) {
	owner := set.Name + "." + op.Name
	failed := len(r.Errors)

	switch {
	case op.RequireOwnership:
		r.errorf("", "%s: @requireOwnership asks the implementation to check what the caller owns, and a @tableOperation has none: name the row's members that hold who it is for in @tableOperation({ by: [...] }) (D108), or write the operation by hand", owner)
	case op.ManualRouteRegistration:
		r.errorf("", "%s: @manualRouteRegistration hands the route to a handler of yours, and @tableOperation to one the build writes: an operation takes one of them", owner)
	case op.Webhook:
		r.errorf("", "%s: @webhook marks an operation a third party calls with its own payload; a @tableOperation's body is a view of its table", owner)
	}
	if op.TypeRef.IsMap || op.TypeRef.IsArrayOfArrays || op.FileDownload != nil {
		r.errorf("", "%s: a @tableOperation answers one @source view of its table, or a list of them", owner)
		return
	}
	result, ok := viewOf(schema, db, owner, "result", op.TypeRef.Name, r)
	if !ok {
		return
	}

	// The arguments: the path's, the body, and a list's sort and search.
	inPath := map[string]bool{}
	for _, match := range pathParameter.FindAllStringSubmatch(op.RestPath, -1) {
		inPath[match[1]] = true
	}
	var path, body, query []*ir.ArgumentDef
	var sortArg, search *ir.ArgumentDef
	for _, arg := range op.Arguments {
		switch {
		case arg == nil:
		case arg.Sort != nil:
			sortArg = arg
		case arg.Search:
			search = arg
		case arg.IsQuery:
			query = append(query, arg)
		case inPath[arg.Name]:
			path = append(path, arg)
		default:
			body = append(body, arg)
		}
	}

	key := result.model.KeyField
	var keyArg *ir.ArgumentDef
	for _, arg := range path {
		if arg.Name == key {
			keyArg = arg
		}
	}
	method := strings.ToUpper(op.HTTPMethod)
	list := op.TypeRef.IsArray
	kind := ""
	switch method {
	case "GET":
		switch {
		case !list && keyArg != nil:
			kind = ir.TableOperationRead
		case list && keyArg == nil:
			kind = ir.TableOperationList
		case list:
			r.errorf("", "%s: a GET whose result is a list of views lists the rows of %s, and its path holds {%s}, one row's key; a GET that reads one row answers one %s", owner, result.source.Name, key, result.view.Name)
		default:
			r.errorf("", "%s: a GET whose result is one view reads a row of %s, and its path holds no {%s}, the table's key; a GET that lists rows answers %s[]", owner, result.source.Name, key, result.view.Name)
		}
	case "POST":
		switch {
		case list:
			r.errorf("", "%s: a POST creates a row of %s and answers its view, not a list", owner, result.source.Name)
		case keyArg != nil:
			r.errorf("", "%s: a POST creates a row of %s, and its path holds {%s}, a row's key; an update of that row is a PATCH", owner, result.source.Name, key)
		default:
			kind = ir.TableOperationCreate
		}
	case "PATCH":
		switch {
		case list:
			r.errorf("", "%s: a PATCH updates a row of %s and answers its view, not a list", owner, result.source.Name)
		case keyArg == nil:
			r.errorf("", "%s: a PATCH updates a row of %s, and its path holds no {%s}, the table's key", owner, result.source.Name, key)
		default:
			kind = ir.TableOperationUpdate
		}
	case "DELETE":
		switch {
		case list:
			r.errorf("", "%s: a DELETE deletes a row of %s and answers its view as it was, not a list", owner, result.source.Name)
		case keyArg == nil:
			r.errorf("", "%s: a DELETE deletes a row of %s, and its path holds no {%s}, the table's key", owner, result.source.Name, key)
		default:
			kind = ir.TableOperationDelete
		}
	case "PUT":
		r.errorf("", "%s: a PUT is refused on a @tableOperation: it would replace the row, or upsert it by a key the caller names, and neither is built (D106); update the row with a PATCH, or write the operation by hand", owner)
	default:
		r.errorf("", "%s: a @tableOperation is a GET, a POST, a PATCH or a DELETE, and this is a %s", owner, method)
	}
	if kind == "" {
		return
	}
	writes := kind != ir.TableOperationRead && kind != ir.TableOperationList
	if !op.Required {
		r.errorf("", "%s: its result is Nullable; a %s answers the row's view, and 404 when the key holds no live row", owner, kind)
	}
	if result.projection && writes {
		r.errorf("", "%s: its result %s is @source(%s), a projection, which is read-only; a %s writes a table", owner, result.view.Name, result.source.Name, kind)
		return
	}

	// The path: the key, and the keys of the table's to-one relations.
	var parents []ir.TableOperationParent
	parentOf := map[string]bool{}
	for _, arg := range path {
		if arg == keyArg {
			if column := result.column(key); column != nil && column.Def != nil && column.Def.TypeRef.Name != arg.TypeRef.Name {
				r.errorf("", "%s: path parameter %s holds %s, and %s's key %s holds %s", owner, arg.Name, typeLabel(arg.TypeRef), result.source.Name, key, column.Def.TypeRef.Name)
			}
			continue
		}
		column := result.column(arg.Name)
		if column == nil || column.Relation == "" {
			r.errorf("", "%s: path parameter %s names neither %s's key, %s, nor the key of one of its relations (%s); a path's parameter holds the row to its parent, and nothing says what %s compares", owner, arg.Name, result.source.Name, key, relationKeys(result.model), arg.Name)
			continue
		}
		if !ir.RelationKeyFits(db.schema.Types[column.Relation], &ir.FieldDef{Name: arg.Name, TypeRef: arg.TypeRef}) {
			r.errorf("", "%s: path parameter %s holds %s, which is not the type of %s's key, the row %s.%s relates to", owner, arg.Name, typeLabel(arg.TypeRef), column.Relation, result.source.Name, column.Field)
			continue
		}
		parents = append(parents, ir.TableOperationParent{Param: arg.Name, Relation: column.Field})
		parentOf[arg.Name] = true
	}
	if kind == ir.TableOperationCreate && keyArg == nil && inPath[key] {
		r.errorf("", "%s: its path holds {%s}, and the operation takes no argument %s", owner, key, key)
	}

	// The other arguments.
	for _, arg := range query {
		if kind == ir.TableOperationList {
			r.errorf("", "%s: argument %s is a QueryParam, and nothing says what a generated list compares it with; filter by a field with FilterBy, or write the list by hand", owner, arg.Name)
		} else {
			r.errorf("", "%s: argument %s is a QueryParam, which a generated %s reads nothing from", owner, arg.Name, kind)
		}
	}
	if kind != ir.TableOperationList {
		if sortArg != nil {
			r.errorf("", "%s: argument %s is a SortBy, which orders a list; a %s has none", owner, sortArg.Name, kind)
		}
		if search != nil {
			r.errorf("", "%s: argument %s is a SearchQuery, which searches a list; a %s has none", owner, search.Name, kind)
		}
		if op.Filter != nil {
			r.errorf("", "%s: argument %s is a FilterBy, which filters a list; a %s has none", owner, op.Filter.Argument, kind)
		}
	}

	checkResultView(owner, result, r)
	callerSets := checkRowRule(owner, kind, op, result, key, parentOf, r)

	var input *tableView
	inputName := ""
	switch kind {
	case ir.TableOperationCreate, ir.TableOperationUpdate:
		if len(body) != 1 {
			names := make([]string, len(body))
			for i, arg := range body {
				names[i] = arg.Name
			}
			found := "none"
			if len(names) > 0 {
				found = strings.Join(names, ", ")
			}
			r.errorf("", "%s: a %s takes one body argument, a @source view of %s, beside its path's parameters, and this takes %s", owner, kind, result.source.Name, found)
			break
		}
		arg := body[0]
		if arg.TypeRef.IsArray || arg.TypeRef.IsMap || !arg.Required {
			r.errorf("", "%s: body argument %s holds %s; a %s takes one required view of %s", owner, arg.Name, typeLabel(arg.TypeRef), kind, result.source.Name)
			break
		}
		view, ok := viewOf(schema, db, owner, "body", arg.TypeRef.Name, r)
		if !ok {
			break
		}
		if view.source != result.source {
			r.errorf("", "%s: its body %s is @source(%s) and its result %s is @source(%s); a @tableOperation reads and writes one table", owner, view.view.Name, view.source.Name, result.view.Name, result.source.Name)
			break
		}
		input, inputName = view, arg.Name
		checkBodyView(owner, kind, input, parentOf, callerSets, r)
	default:
		for _, arg := range body {
			if method == "GET" {
				r.errorf("", "%s: argument %s is neither in the path nor a SortBy, a SearchQuery or a FilterBy; a generated %s reads nothing else", owner, arg.Name, kind)
			} else {
				r.errorf("", "%s: argument %s is a body argument, and a %s takes none", owner, arg.Name, kind)
			}
		}
	}

	if kind == ir.TableOperationList {
		checkListOperation(owner, op, result, sortArg, search, r)
	}

	filled := &ir.TableOperation{
		Kind: kind, Database: db.name, Type: result.source.Name, Projection: result.projection,
		Parents: parents, Input: inputName,
		By: op.TableOperation.By, OverridePermission: op.TableOperation.OverridePermission,
	}
	if keyArg != nil {
		filled.Key = key
	}
	if written := op.TableOperation; len(r.Errors) == failed && !tableOperationUnsaid(written) && !tableOperationsEqual(written, filled) {
		r.errorf("", "%s: its tableOperation says %s, and its declaration makes it %s; leave the members out, the loader fills them", owner, describeTableOperation(written), describeTableOperation(filled))
	}
	if len(r.Errors) == failed {
		op.TableOperation = filled
	}
}

// relationKeys lists the keys of a table's to-one relations, for a
// message: teamId, assigneeId; "it has none" for a table without one.
func relationKeys(model *sqlutil.TableModel) string {
	var keys []string
	for _, c := range model.Columns {
		if c.Relation != "" {
			keys = append(keys, c.Field+"Id")
		}
	}
	if len(keys) == 0 {
		return "it has none"
	}
	return strings.Join(keys, ", ")
}

// checkResultView holds a result's view to what a handler can fill: every
// field reads a column.
func checkResultView(owner string, result *tableView, r *Result) {
	for _, f := range result.view.Fields {
		switch {
		case f.Virtual:
			r.errorf("", "%s: its result %s has the @virtual field %s, which no column of %s holds and no implementation of a @tableOperation fills; leave the field out of the view, or write the operation by hand", owner, result.view.Name, f.Name, result.source.Name)
		case result.column(f.Name) == nil:
			r.errorf("", "%s: its result %s has the field %s, which the repository of %s reads no column for", owner, result.view.Name, f.Name, result.source.Name)
		}
	}
}

// subjectMember reports whether a row rule may name the column (D108): it
// holds one subject the repositories filter by, a UUID, a to-one
// relation's key or text, and is the row's own to say: createdBy alone
// among the audit fields, no field a behavior owns, no secret.
func subjectMember(source *ir.TypeDef, c *sqlutil.ColumnModel) bool {
	_, anchors := source.FieldBehavior(c.Field)
	return (c.Filter == sqlutil.FilterUUID || c.Filter == sqlutil.FilterString) &&
		(c.Audit == "" || c.Audit == sqlutil.AuditCreatedBy) &&
		!c.Secret && !c.Encrypted && !c.Internal && !anchors && (c.Def == nil || !c.Def.Owned())
}

// checkRowRule holds a table operation's row rule to its table (D108): by
// names members of the row that hold a subject, as a view names them (an
// own field, a to-one relation's key, createdBy, the key itself), on a
// route that requires a caller, since the rule compares the row with
// who is calling. A read, a list, an update and a delete then reach only
// the rows one member names the caller in. A create sets each member but
// createdBy, which the repository stamps, to the caller: it returns those,
// which the body may not carry, and refuses the key, which the table
// generates, and a relation the path sets.
func checkRowRule(owner, kind string, op *ir.FieldDef, result *tableView, key string, parents map[string]bool, r *Result) map[string]bool {
	rule := op.TableOperation
	if rule == nil || len(rule.By) == 0 {
		if rule != nil && rule.OverridePermission != "" {
			r.errorf("", "%s: its @tableOperation names overridePermission %q, which lifts a row rule, and names no by; list the row's members that name who may reach it", owner, rule.OverridePermission)
		}
		return nil
	}
	if !op.HasUserClause() {
		r.errorf("", "%s: its row rule reaches the rows that name the caller, and the route admits a request with none; guard it with @auth or @requirePermission, or put it in an Authenticated set (D108)", owner)
	}
	if result.projection {
		r.errorf("", "%s: its row rule is over %s, a projection, and a rule is held to a table's own columns so far (D108); write the operation by hand", owner, result.source.Name)
		return nil
	}
	var known []string
	for i := range result.model.Columns {
		if c := &result.model.Columns[i]; subjectMember(result.source, c) {
			known = append(known, viewMember(c))
		}
	}
	listed := "it has none"
	if len(known) > 0 {
		listed = strings.Join(known, ", ")
	}
	sets := map[string]bool{}
	seen := map[string]bool{}
	for _, name := range rule.By {
		c := result.column(name)
		again := seen[name]
		seen[name] = true
		switch {
		case again:
			r.errorf("", "%s: by names %s twice", owner, name)
		case c == nil:
			r.errorf("", "%s: by names %s, which is no member of %s; a row rule names a member that holds a subject (%s)", owner, name, result.source.Name, listed)
		case c.Audit != "" && c.Audit != sqlutil.AuditCreatedBy:
			r.errorf("", "%s: by names %s, which the repository stamps at a write and says nothing of who the row is for; name createdBy, or a member of the row (%s)", owner, name, listed)
		case !subjectMember(result.source, c):
			r.errorf("", "%s: by names %s, which holds no subject a row rule compares the caller with: text, a UUID, a to-one relation's key or createdBy, none of them secret or a behavior's (%s)", owner, name, listed)
		case kind != ir.TableOperationCreate || c.Audit == sqlutil.AuditCreatedBy:
		case name == key:
			r.errorf("", "%s: by names %s, the key of %s, and a create's row rule sets each member it names to the caller: a create names no key", owner, name, result.source.Name)
		case parents[name]:
			r.errorf("", "%s: by names %s, which the path's {%s} sets, and a create's row rule sets each member it names to the caller: one of them sets it", owner, name, name)
		default:
			sets[name] = true
		}
	}
	return sets
}

// viewMember is the name a view holds a column under: its field, or a
// to-one relation's key.
func viewMember(c *sqlutil.ColumnModel) string {
	if c.Relation != "" {
		return c.Field + "Id"
	}
	return c.Field
}

// checkBodyView holds a create's or an update's body to the columns a
// caller may set (D106). A create also sets every column the table
// requires and gives no value itself; an update's members are all
// optional. parents are the path parameters that set a relation, and
// callerSets the members a create's row rule sets to the caller (D108):
// the body may set neither.
func checkBodyView(owner, kind string, body *tableView, parents, callerSets map[string]bool, r *Result) {
	set := map[string]bool{}
	for _, f := range body.view.Fields {
		if f.Virtual {
			r.errorf("", "%s: its body %s has the @virtual field %s, which no column of %s holds, so a %s has nowhere to write it", owner, body.view.Name, f.Name, body.source.Name, kind)
			continue
		}
		column := body.column(f.Name)
		if column == nil {
			r.errorf("", "%s: its body %s has the field %s, which the repository of %s writes no column for", owner, body.view.Name, f.Name, body.source.Name)
			continue
		}
		set[f.Name] = true
		_, anchors := body.source.FieldBehavior(column.Field)
		why := ""
		switch {
		case column.Key && (column.AutoGenerated || column.Def == nil):
			why = "the key, which " + body.source.Name + " generates"
		case column.Key && kind == ir.TableOperationUpdate:
			why = "the key, which names the row and never changes"
		case column.Audit != "":
			why = "an audit field, which the repository stamps"
		case column.Internal || column.Name == sqlutil.VersionColumn:
			why = "internal metadata, which no write sets"
		case anchors || (column.Def != nil && column.Def.Owned()):
			why = "a field a behavior owns, which only its operations change"
		case parents[f.Name]:
			why = "the relation the path's {" + f.Name + "} sets"
		case callerSets[f.Name]:
			why = "the member its row rule sets to the caller"
		}
		if why != "" {
			r.errorf("", "%s: its body %s sets %s, %s; leave the field out of the view", owner, body.view.Name, f.Name, why)
			continue
		}
		if kind == ir.TableOperationUpdate && f.Required {
			r.errorf("", "%s: its body %s requires %s; an update's members are all optional, each one left out leaving its column as it is: write %s: Nullable<…>", owner, body.view.Name, f.Name, f.Name)
		}
	}
	if kind != ir.TableOperationCreate {
		return
	}
	var missing []string
	for _, c := range body.model.Columns {
		member := c.Field
		if c.Relation != "" {
			member += "Id"
		}
		_, anchors := body.source.FieldBehavior(c.Field)
		if set[member] || parents[member] || callerSets[member] || c.Internal || c.Audit != "" || anchors || (c.Def != nil && c.Def.Owned()) {
			continue
		}
		if !c.Required || c.AutoGenerated || c.Default != nil || c.DefaultsToNow() || (c.Key && (c.Def == nil || c.Class.UUIDLike)) {
			continue
		}
		missing = append(missing, member)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		r.errorf("", "%s: its body %s leaves out %s, which %s requires and gives no default: no request could create a row; add %s to the view, or name it in the path", owner, body.view.Name, strings.Join(missing, ", "), body.source.Name, pluralIt(len(missing)))
	}
}

// pluralIt is "it" for one and "them" for more.
func pluralIt(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// checkListOperation holds a generated list to its page, its order and
// its search (D106): it reads the repository's findPage, so it is
// @paginated, sorts by columns the repository sorts by, and searches only
// a table with search fields. Its filter's fields are held to the view's
// columns by checkFilters, as any filter over a @source item is.
func checkListOperation(owner string, op *ir.FieldDef, result *tableView, sortArg, search *ir.ArgumentDef, r *Result) {
	if op.Page == nil {
		r.errorf("", "%s: a generated list is @paginated: it reads a page of %s's rows, never the whole table", owner, result.source.Name)
	}
	if sortArg != nil {
		for _, field := range sortArg.Sort {
			column := result.column(field)
			if column == nil {
				// checkSorts and checkResultView name a field the view or
				// the table lacks.
				continue
			}
			if !column.Sortable {
				r.errorf("", "%s: sort argument %s names %s, a column the repository of %s does not page by", owner, sortArg.Name, field, result.source.Name)
			}
		}
	}
	if search != nil && !result.model.Search {
		r.errorf("", "%s: argument %s is a SearchQuery, and %s has no @searchField for it to search", owner, search.Name, result.source.Name)
	}
	if op.Filter != nil {
		for _, field := range op.Filter.Fields {
			if field == nil {
				continue
			}
			if column := result.column(field.Name); column != nil && column.Filter == sqlutil.FilterNone {
				r.errorf("", "%s: filter argument %s names %s, a column the repository of %s keeps no filter for", owner, op.Filter.Argument, field.Name, result.source.Name)
			}
		}
	}
}

// tableOperationUnsaid reports whether a written TableOperation says
// nothing, as the decorator's does.
func tableOperationUnsaid(t *ir.TableOperation) bool {
	return t == nil || (t.Kind == "" && t.Database == "" && t.Type == "" && !t.Projection && t.Key == "" && len(t.Parents) == 0 && t.Input == "")
}

func tableOperationsEqual(a, b *ir.TableOperation) bool {
	return a.Kind == b.Kind && a.Database == b.Database && a.Type == b.Type && a.Projection == b.Projection &&
		a.Key == b.Key && a.Input == b.Input && slices.Equal(a.Parents, b.Parents)
}

// describeTableOperation words a TableOperation for a message: a create of
// shop-db.Order.
func describeTableOperation(t *ir.TableOperation) string {
	kind := t.Kind
	if kind == "" {
		kind = "an operation"
	} else {
		kind = "a " + kind
	}
	return kind + " of " + t.Database + "." + t.Type
}
