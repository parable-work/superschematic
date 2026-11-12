package apigen

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// An API's table operations (D106), the methods it marks @tableOperation,
// are the build's to serve: the Go server's router mounts each at its
// route behind the steps any route runs, on a generated handler that reads
// or writes one table of the API's database through its ORM, in one
// transaction, as the request's caller. The implementation the project
// writes has no method for them. The OpenAPI document, the SDKs, the
// permission catalog and the tool documents read them as any other
// operation, so they stay in APIOutput.Endpoints, and ImplementedOutput
// moves them to APIOutput.TableEndpoints for the Go server's writers and
// models tables.go, the file that holds their handlers, as TablesInfo.
//
// The loader has held each operation to what a handler can serve
// (verify.checkTableOperations) and filled ir.TableOperation. What is
// modelled here is Go's alone: how the generated Go types hold each value
// of a view and of a row, so that a view is filled from a row, and a row
// or an update from a body, member by member, with no reflection and no
// JSON between them. An operation whose views the Go handlers cannot fill
// that way stays one the project implements, and the build warns of it
// (UnservedTableOperations, D106's last rule); IR the loader did not fill
// is an error that names the operation.

// TableOperation is the table operation the endpoint serves (D106): the
// loader's record on its operation, nil for every other endpoint.
func (e EndpointInfo) TableOperation() *ir.TableOperation {
	if e.Operation == nil {
		return nil
	}
	return e.Operation.TableOperation
}

// ServedTable is the table operation tables.go serves the endpoint from:
// TableOperation, but nil for one the Go server writes no handler for yet
// (TableByHand), which the project implements as any operation of its
// own.
func (e EndpointInfo) ServedTable() *ir.TableOperation {
	if e.TableByHand {
		return nil
	}
	return e.TableOperation()
}

// TableFunc is the function of tables.go that serves the endpoint's table
// operation, which its handler factory calls: table and the handler's
// name, tableIssuesGetIssue.
func (e EndpointInfo) TableFunc() string {
	return "table" + strings.TrimSuffix(e.HandlerName, "Handler")
}

// HasTableEndpoints reports whether the Go server serves a table operation
// of the API from tables.go (ServedTableEndpoints): one of its
// TableEndpoints in the view ImplementedOutput returns, or of its
// Endpoints that it would move there. Its Config then takes the ORM of its
// database, and the module holds tables.go.
func (o *APIOutput) HasTableEndpoints() bool {
	return len(o.ServedTableEndpoints()) > 0
}

// TakesDatabase reports whether the Config of an API that is not public
// takes the ORM of its database: its behavior operations (D103) and its
// table operations (D106) run through it.
func (o *APIOutput) TakesDatabase() bool {
	return o.HasBehaviorEndpoints() || o.HasTableEndpoints()
}

// TablesWrite reports whether a table operation of the view creates or
// updates a row: tables.go then reads the database's refusal of one.
func (o *APIOutput) TablesWrite() bool {
	return slices.ContainsFunc(o.TableEndpoints, func(ep EndpointInfo) bool {
		kind := ep.TableOperation().Kind
		return kind == ir.TableOperationCreate || kind == ir.TableOperationUpdate
	})
}

// TablesDelete reports whether a table operation of the view deletes a
// row: tables.go then reads the database's refusal of one.
func (o *APIOutput) TablesDelete() bool {
	return slices.ContainsFunc(o.TableEndpoints, func(ep EndpointInfo) bool {
		return ep.TableOperation().Kind == ir.TableOperationDelete
	})
}

// TablesRequireCaller reports whether the handler factory of a table
// operation of the view checks for a caller itself
// (routeData.TableRequiresCaller): tables.go then holds requireTableCaller.
func (o *APIOutput) TablesRequireCaller() bool {
	return slices.ContainsFunc(o.TableEndpoints, func(ep EndpointInfo) bool {
		return newRouteData(o, ep).TableRequiresCaller()
	})
}

// tableDatabase is the database the endpoints' table operations read and
// write, the API's database, or "" when none is one.
func tableDatabase(endpoints []EndpointInfo) string {
	for _, ep := range endpoints {
		if to := ep.TableOperation(); to != nil && to.Database != "" {
			return to.Database
		}
	}
	return ""
}

// setTableDeps gives Deps what tables.go imports beside the ORM: the Go
// types module of the API's database, whose rows the handlers read and
// write, under the name of its service. An API with workers has it already
// (D53).
func (o *APIOutput) setTableDeps() {
	marked := slices.ContainsFunc(o.Endpoints, func(ep EndpointInfo) bool { return ep.TableOperation() != nil })
	if (!marked && len(o.TableEndpoints) == 0) || o.Deps.Database == "" {
		return
	}
	if o.Deps.DatabaseTypes == "" {
		o.Deps.DatabaseTypes = o.Naming.OrDefault().GoTypesModule(o.Deps.Database)
	}
	if o.Deps.DatabaseTypesAlias == "" {
		o.Deps.DatabaseTypesAlias = toPackageName(o.Deps.Database)
	}
	o.AddIndirectModules([]string{o.Deps.DatabaseTypes})
}

// tableSource is what the table operations' handlers are modelled from.
type tableSource struct {
	schema       *ir.Schema
	dependencies map[string]*ir.Schema
	types        *typeMapper
}

// TablesInfo is what tables.go is written from: a function per table
// operation, a function per view that fills it from a row, and what the
// refusals of a table's writes name.
type TablesInfo struct {
	// Database is the API's database, and Rules whether one of its tables
	// composes behaviors, whose writes run through the core (D103).
	Database string
	Rules    bool

	// Handlers are the table operations' functions, in the endpoints'
	// order; Views the functions that fill a view from a row, by view; Rows
	// what each table's refusals name, by table; and Filters the endpoints
	// whose filter type tables.go declares.
	Handlers []TableHandler
	Views    []TableView
	Rows     []TableRows
	Filters  []EndpointInfo

	// What tables.go imports and declares beside them.
	UsesJSON     bool
	UsesTime     bool
	UsesPaging   bool
	UsesContains bool
	UsesTexts    bool
	UsesMembers  bool
	// UsesRowRules: an operation holds a row rule (D108), so tables.go
	// reads the request's caller; UsesRowLift, one names the permission
	// that lifts it; UsesRowCreate, a create sets a member to the caller.
	UsesRowRules  bool
	UsesRowLift   bool
	UsesRowCreate bool
}

// TableRows is what the refusals of a table's writes name (D106): the
// table's type, and for each column a write may break a constraint on, the
// member a body sets it by and, for a foreign key, the type it points at.
type TableRows struct {
	Var     string
	Type    string
	Columns []TableColumn
}

// TableColumn is one column of TableRows.
type TableColumn struct {
	Column  string
	Member  string
	Related string
}

// TableView is the function that fills a view from a row of its table.
type TableView struct {
	Func    string
	View    string
	RowType string
	Table   string
	Lines   []string
}

// TableHandler is the function that serves one table operation.
type TableHandler struct {
	Endpoint EndpointInfo
	Kind     string
	Func     string
	Type     string

	// Params are the function's parameters after ctx and db, as Go
	// declares them, and Result its result's type beside the error, Zero
	// that type's zero value.
	Params string
	Result string
	Zero   string

	// Before are the statements before the transaction, which may return
	// an error, and Body those inside it, over ctx, tx and result.
	Before []string
	Body   []string

	// Rows is the table's TableRows, and Parents the columns of the path's
	// parents, as Go lists them after it, each with a leading comma.
	Rows    string
	Parents string
}

// goMember is how the generated Go types hold one value of a type or of a
// row: a pointer or a value, and for an enum the enum's name, whose Go type
// is a string type of the package that declares it.
type goMember struct {
	pointer bool
	enum    string
}

// tableBuilder models the handlers of one API's table operations.
type tableBuilder struct {
	output *APIOutput
	source *tableSource
	db     *ir.Schema
	dbName string
	// dbTypes maps the database's own types, and alias is the name
	// tables.go imports its Go types module under.
	dbTypes *typeMapper
	alias   string
	tables  map[string]*sqlutil.TableModel

	info  *TablesInfo
	views map[string]bool
	rows  map[string]bool
}

// UnservedTableOperation is a table operation the Go server writes no
// handler for yet (D106): the operation as the loader names it,
// Set.operation, and why, worded to follow its name ("its view IssueView
// holds labels, string[], and ..."). It stays a method of the
// implementation's interface and its scaffold, to be written by hand, and
// the build warns of it.
type UnservedTableOperation struct {
	Operation string
	Reason    string
}

// tablePlan is what the Go server does with an API's table operations: the
// ones tables.go serves, modelled as info, and the ones it leaves to the
// implementation, each with its reason.
type tablePlan struct {
	info     *TablesInfo
	served   []EndpointInfo
	unserved []UnservedTableOperation
	// byHand holds the operations of unserved.
	byHand map[*ir.FieldDef]bool
}

// UnservedTableOperations lists the table operations of output the Go
// server writes no handler for yet (D106), in the endpoints' order, as
// ImplementedOutput decides them: each stays an operation the project
// implements, in the interface, the scaffold and the routes, and the build
// warns of it. IR whose @tableOperation the loader did not fill is an
// error, as it is ImplementedOutput's.
func UnservedTableOperations(output *APIOutput) ([]UnservedTableOperation, error) {
	plan, err := planTables(output, false)
	if err != nil || plan == nil {
		return nil, err
	}
	return plan.unserved, nil
}

// ServedTableEndpoints are the table operations the Go server serves from
// tables.go: TableEndpoints in the view ImplementedOutput returns, and in
// any other output the ones it would move there. An output whose plan
// fails holds every one, so what reads it wires the database, and the
// writers report the error.
func (o *APIOutput) ServedTableEndpoints() []EndpointInfo {
	if len(o.TableEndpoints) > 0 {
		return o.TableEndpoints
	}
	plan, err := planTables(o, false)
	if err != nil {
		var all []EndpointInfo
		for _, ep := range o.Endpoints {
			if ep.ServedTable() != nil {
				all = append(all, ep)
			}
		}
		return all
	}
	if plan == nil {
		return nil
	}
	return plan.served
}

// planTables decides, for each table operation among output's Endpoints,
// whether tables.go serves it, and models tables.go from the ones it does:
// the one decision ImplementedOutput and UnservedTableOperations share. It
// is nil for an output with none. strict holds the output to what writing
// tables.go needs, its Deps on the operations' database with its Go types
// module, which the dispatch layer sets before it writes.
func planTables(output *APIOutput, strict bool) (*tablePlan, error) {
	var candidates []EndpointInfo
	for _, ep := range output.Endpoints {
		if ep.ServedTable() != nil {
			candidates = append(candidates, ep)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	source := output.tables
	dbName := tableDatabase(candidates)
	first := candidates[0].Namespace + "." + candidates[0].Name
	if source != nil {
		first = (&tableBuilder{source: source}).owner(candidates[0])
	}
	if dbName == "" {
		return nil, fmt.Errorf("apigen: operation %s: its @tableOperation names no database; the loader fills it from the operation's views (D106)", first)
	}
	if source == nil || source.dependencies[dbName] == nil {
		return nil, fmt.Errorf("apigen: operation %s: its @tableOperation reads and writes a table of %s, whose schema the generator was not given (Options.Dependencies), so the Go server cannot write its handler (D106)", first, dbName)
	}
	if strict && output.Deps.Database != dbName {
		return nil, fmt.Errorf("apigen: operation %s: its @tableOperation reads and writes a table of %s, and the Go server's Deps hold the ORM of %q (D106)", first, dbName, output.Deps.Database)
	}
	db := source.dependencies[dbName]
	models, err := sqlutil.TablesOf(db, nil)
	if err != nil {
		return nil, fmt.Errorf("apigen: reading the tables of %s, the database of %s: %w", dbName, output.SchemaName, err)
	}
	b := &tableBuilder{
		output:  output,
		source:  source,
		db:      db,
		dbName:  dbName,
		dbTypes: newTypeMapper(db, nil),
		alias:   output.Deps.DatabaseTypesAlias,
		tables:  make(map[string]*sqlutil.TableModel, len(models)),
		info:    &TablesInfo{Database: dbName, Rules: output.Deps.Rules},
		views:   map[string]bool{},
		rows:    map[string]bool{},
	}
	if b.alias == "" {
		if strict {
			return nil, fmt.Errorf("apigen: %s serves table operations of %s, and its Deps name no Go types module of it", output.SchemaName, dbName)
		}
		b.alias = toPackageName(dbName)
	}
	for i := range models {
		b.tables[models[i].Type] = &models[i]
	}
	plan := &tablePlan{byHand: map[*ir.FieldDef]bool{}}
	for _, endpoint := range candidates {
		// What an operation left to the implementation added to the
		// model before it was, a view's function or an import, goes
		// with it.
		info, views, rows := *b.info, maps.Clone(b.views), maps.Clone(b.rows)
		handler, err := b.handler(endpoint)
		var left *unservedError
		if errors.As(err, &left) {
			*b.info, b.views, b.rows = info, views, rows
			plan.unserved = append(plan.unserved, UnservedTableOperation{Operation: b.owner(endpoint), Reason: left.reason})
			plan.byHand[endpoint.Operation] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		b.info.Handlers = append(b.info.Handlers, *handler)
		if endpoint.Filter != nil {
			b.info.Filters = append(b.info.Filters, endpoint)
		}
		plan.served = append(plan.served, endpoint)
	}
	if len(plan.served) > 0 {
		sort.Slice(b.info.Views, func(i, j int) bool { return b.info.Views[i].View < b.info.Views[j].View })
		sort.Slice(b.info.Rows, func(i, j int) bool { return b.info.Rows[i].Type < b.info.Rows[j].Type })
		plan.info = b.info
	}
	return plan, nil
}

// tableView is a view of a table operation held to its table: the type,
// and the table's model.
type tableView struct {
	def   *ir.TypeDef
	model *sqlutil.TableModel
	table *ir.TypeDef
}

// column finds the column a view's field reads or sets, as the loader
// does: the column of the field's own name, or a to-one relation's, for
// its key (assigneeId).
func (v *tableView) column(field string) *sqlutil.ColumnModel {
	for i := range v.model.Columns {
		c := &v.model.Columns[i]
		if (c.Relation == "" && c.Field == field) || (c.Relation != "" && c.Field+"Id" == field) {
			return c
		}
	}
	return nil
}

// member is the name a body or a view holds a column under: its field, or
// a relation's key.
func member(c *sqlutil.ColumnModel) string {
	if c.Relation != "" {
		return c.Field + "Id"
	}
	return c.Field
}

// viewOf finds a view an operation names among the API's own types and
// holds it to the operation's table.
func (b *tableBuilder) viewOf(owner, what, typeName string, to *ir.TableOperation) (*tableView, error) {
	def := b.source.schema.Types[typeName]
	if def == nil {
		def = b.source.schema.Inputs[typeName]
	}
	if def == nil || def.Source == nil {
		return nil, fmt.Errorf("apigen: operation %s: its %s %s is no @source view of %s; the loader holds a @tableOperation's views to its table (D106)", owner, what, typeName, b.dbName)
	}
	model := b.tables[to.Type]
	table := b.db.Types[to.Type]
	if model == nil || table == nil {
		return nil, fmt.Errorf("apigen: operation %s: its @tableOperation names %s, which is no table of %s with a repository (D106)", owner, to.Type, b.dbName)
	}
	return &tableView{def: def, model: model, table: table}, nil
}

// handler models the function of one table operation.
func (b *tableBuilder) handler(endpoint EndpointInfo) (*TableHandler, error) {
	to := endpoint.TableOperation()
	owner := b.owner(endpoint)
	if !slices.Contains(ir.TableOperationKinds, to.Kind) || to.Type == "" || to.Database == "" {
		return nil, fmt.Errorf("apigen: operation %s: its @tableOperation has no kind, table and database; the loader fills them from the operation's method, path and views (D106)", owner)
	}
	if to.Database != b.dbName {
		return nil, fmt.Errorf("apigen: operation %s: its @tableOperation reads and writes a table of %s, and the API's other table operations one of %s; an API serves the tables of one database (D106)", owner, to.Database, b.dbName)
	}
	if to.Projection {
		return nil, unserved("its result %s reads the projection %s, and the Go server writes handlers over a table's repository alone so far", endpoint.OutputType, to.Type)
	}
	if len(to.Starts) > 0 {
		return nil, unserved("its body names the state the row starts in, %s, and the Go server writes no create that makes a Workflow's transition so far (D109)", strings.Join(to.Starts, ", "))
	}
	result, err := b.viewOf(owner, "result", endpoint.OutputType, to)
	if err != nil {
		return nil, err
	}
	asView, err := b.view(owner, result)
	if err != nil {
		return nil, err
	}
	rows, err := b.tableRows(owner, result)
	if err != nil {
		return nil, err
	}

	h := &TableHandler{
		Endpoint: endpoint,
		Kind:     to.Kind,
		Func:     endpoint.TableFunc(),
		Type:     to.Type,
		Result:   "*types." + endpoint.OutputType,
		Zero:     "nil",
		Rows:     rows,
	}
	repo := "tx.Get" + to.Type + "Repository()"

	// The path: the key and the parents, each a condition on the row.
	var params []string
	var conditions []string
	keyed := false
	parents := map[string]*sqlutil.ColumnModel{}
	for _, parent := range to.Parents {
		column := result.column(parent.Param)
		if column == nil || column.Relation == "" || column.Field != parent.Relation {
			return nil, fmt.Errorf("apigen: operation %s: its @tableOperation holds the row to %s by {%s}, which is no key of a relation of %s (D106)", owner, parent.Relation, parent.Param, to.Type)
		}
		parents[parent.Param] = column
		h.Parents += ", " + strconv.Quote(column.Name)
	}
	for _, param := range endpoint.PathParams {
		params = append(params, param.GoName+" "+param.GoType)
		switch column, isParent := parents[param.Name]; {
		case isParent:
			if column.Filter != sqlutil.FilterUUID || !param.IsUUID {
				return nil, unserved("path parameter %s holds the row to %s by a key that is no UUID, which the Go repositories keep no filter for", param.Name, column.Field)
			}
			conditions = append(conditions, codegen.ToPascalCase(column.Field)+"ID: &orm.UUIDFilter{Eq: &"+param.GoName+"}")
		case param.Name == to.Key && to.Key != "":
			column := result.model.KeyColumn()
			if column == nil || column.Field != to.Key {
				return nil, fmt.Errorf("apigen: operation %s: its @tableOperation's key is %s, which is not the key of %s (D106)", owner, to.Key, to.Type)
			}
			condition, err := b.keyCondition(column, param)
			if err != nil {
				return nil, err
			}
			conditions = append(conditions, condition)
			keyed = true
		default:
			return nil, fmt.Errorf("apigen: operation %s: path parameter %s names neither the key of %s nor the key of one of its relations; the loader fills a @tableOperation's key and parents (D106)", owner, param.Name, to.Type)
		}
	}
	if len(parents) != len(to.Parents) || len(conditions) != len(endpoint.PathParams) {
		return nil, fmt.Errorf("apigen: operation %s: its @tableOperation's parents are not its path's parameters (D106)", owner)
	}
	needsKey := to.Kind == ir.TableOperationRead || to.Kind == ir.TableOperationUpdate || to.Kind == ir.TableOperationDelete
	if needsKey != keyed {
		return nil, fmt.Errorf("apigen: operation %s: a %s of %s %s; the loader fills a @tableOperation's key (D106)", owner, to.Kind, to.Type, map[bool]string{true: "names its row by the table's key, and its path holds none", false: "takes no key, and its path holds one"}[needsKey])
	}
	where := "&orm." + to.Type + "Filter{" + strings.Join(conditions, ", ") + "}"
	find := []string{
		"row, err := repo.FindOne(ctx, " + where + ", nil)",
		"if err != nil {",
		"return err",
		"}",
	}
	// A row rule (D108) holds beside the key and the parents: a row that
	// names another caller is the 404 of one that is not there.
	reach, err := b.reach(owner, result, to, []string{"return orm.ErrNotFound"})
	if err != nil {
		return nil, err
	}
	if len(reach) > 0 {
		find = append(append([]string{"where := " + where}, reach...),
			"row, err := repo.FindOne(ctx, where, nil)",
			"if err != nil {",
			"return err",
			"}")
	}
	rowKey, err := b.rowKey(owner, result)
	if err != nil {
		return nil, err
	}

	switch to.Kind {
	case ir.TableOperationRead:
		if err := b.noArguments(owner, endpoint, to.Kind); err != nil {
			return nil, err
		}
		h.Body = append(h.Body, "repo := "+repo)
		h.Body = append(h.Body, find...)
		h.Body = append(h.Body, "view := "+asView+"(row)", "result = &view", "return nil")

	case ir.TableOperationDelete:
		if err := b.noArguments(owner, endpoint, to.Kind); err != nil {
			return nil, err
		}
		h.Body = append(h.Body, "repo := "+repo)
		h.Body = append(h.Body, find...)
		h.Body = append(h.Body,
			"if err := repo.DeleteOne(ctx, "+rowKey+"); err != nil {",
			"return err",
			"}",
			"view := "+asView+"(row)", "result = &view", "return nil")

	case ir.TableOperationList:
		if err := b.list(owner, endpoint, h, result, to, conditions, asView, repo, &params); err != nil {
			return nil, err
		}

	case ir.TableOperationCreate, ir.TableOperationUpdate:
		if !endpoint.HasInput || endpoint.InputType == "" || endpoint.HasFileUpload || len(endpoint.ScalarArgs) > 0 || len(endpoint.QueryParams) > 0 || endpoint.Filter != nil || endpoint.Page != nil {
			return nil, fmt.Errorf("apigen: operation %s: a %s takes one body, a @source view of %s, beside its path's parameters; the loader holds a @tableOperation to it (D106)", owner, to.Kind, to.Type)
		}
		body, err := b.viewOf(owner, "body", endpoint.InputType, to)
		if err != nil {
			return nil, err
		}
		params = append(params, "input *types."+endpoint.InputType, "raw json.RawMessage")
		b.info.UsesJSON = true
		h.Body = append(h.Body, "repo := "+repo)
		if to.Kind == ir.TableOperationCreate {
			if err := b.create(owner, h, body, parents, endpoint, asView, to); err != nil {
				return nil, err
			}
		} else {
			h.Body = append(h.Body, find...)
			if err := b.update(owner, h, body, rowKey, asView); err != nil {
				return nil, err
			}
		}
	}

	for _, param := range params {
		h.Params += ", " + param
	}
	return h, nil
}

// reach are the statements that hold where, a filter of the operation's
// table, to the rows its row rule reaches for the request's caller (D108):
// the rows one of the rule's members holds the caller's id in, unless the
// caller holds the rule's override permission. none are the statements
// that answer a caller no row can name, one with no id. No statements for
// an operation without a rule. A member that holds text is not served
// yet: the Go handlers compare the caller with a UUID alone.
func (b *tableBuilder) reach(owner string, result *tableView, to *ir.TableOperation, none []string) ([]string, error) {
	if len(to.By) == 0 {
		if to.OverridePermission != "" {
			return nil, fmt.Errorf("apigen: operation %s: its @tableOperation names the override permission %s and no row rule; the loader refuses it (D108)", owner, to.OverridePermission)
		}
		return nil, nil
	}
	filter := "orm." + to.Type + "Filter"
	var named []string
	for _, name := range to.By {
		column := result.column(name)
		if column == nil {
			return nil, fmt.Errorf("apigen: operation %s: its row rule names %s, which is no member of %s; the loader holds a @tableOperation's rule to its table (D108)", owner, name, to.Type)
		}
		if column.Filter != sqlutil.FilterUUID {
			return nil, unserved("its row rule names %s, which holds text, and the Go server compares the caller with a member that holds a UUID alone so far (D108)", name)
		}
		member := codegen.ToPascalCase(column.Field)
		if column.Relation != "" {
			member += "ID"
		}
		named = append(named, "{"+member+": &orm.UUIDFilter{Eq: &caller}}")
	}
	b.info.UsesRowRules = true
	open := "{"
	if to.OverridePermission != "" {
		b.info.UsesRowLift = true
		open = "if !tableLifted(ctx, " + strconv.Quote(to.OverridePermission) + ") {"
	}
	lines := []string{open, "caller, named := tableCallerID(ctx)", "if !named {"}
	lines = append(lines, none...)
	return append(lines, "}",
		"where.AllOf = append(where.AllOf, "+filter+"{AnyOf: []"+filter+"{"+strings.Join(named, ", ")+"}})",
		"}"), nil
}

// owner names the endpoint's operation as the loader does, Set.operation,
// for a message.
func (b *tableBuilder) owner(endpoint EndpointInfo) string {
	for _, set := range b.source.schema.OperationSets {
		if set != nil && slices.Contains(set.Operations, endpoint.Operation) {
			return set.Name + "." + endpoint.Name
		}
	}
	return endpoint.Namespace + "." + endpoint.Name
}

// noArguments refuses an argument of a read or a delete beside its path's.
func (b *tableBuilder) noArguments(owner string, endpoint EndpointInfo, kind string) error {
	if endpoint.HasInput || len(endpoint.ScalarArgs) > 0 || len(endpoint.QueryParams) > 0 || endpoint.Filter != nil || endpoint.Page != nil {
		return fmt.Errorf("apigen: operation %s: a %s takes its path's parameters alone; the loader holds a @tableOperation to it (D106)", owner, kind)
	}
	return nil
}

// keyCondition is the filter's member that keeps the row whose key is the
// path parameter's.
func (b *tableBuilder) keyCondition(column *sqlutil.ColumnModel, param Param) (string, error) {
	name := codegen.ToPascalCase(column.Field)
	switch {
	case column.Filter == sqlutil.FilterUUID && param.IsUUID:
		return name + ": &orm.UUIDFilter{Eq: &" + param.GoName + "}", nil
	case column.Filter == sqlutil.FilterString && !param.IsInt && !param.IsFloat && !param.IsBool && !param.IsDateTime && !param.IsUUID:
		b.info.UsesTexts = true
		return name + ": &orm.StringFilter{Eq: tableText(&" + param.GoName + ")}", nil
	case column.Filter == sqlutil.FilterInt && param.IsInt:
		return name + ": &orm.Int64Filter{Eq: &" + param.GoName + "}", nil
	}
	return "", unserved("the key %s of its table is neither a UUID, text nor an integer, and the Go server finds a row by no other key so far", column.Field)
}

// goMemberOf is how the Go types hold field, a field of a type of the
// schema types maps, as typegen declares it: a value, or a pointer for a
// field that may be absent, but for text and a number, which Go holds as
// their zero, and an integer or a boolean with a default. ok is false for
// a list, a map and a value that is neither a scalar, text, a number, a
// boolean nor an enum.
func goMemberOf(types *typeMapper, field *ir.FieldDef) (goMember, bool) {
	if field == nil || field.TypeRef.IsArray || field.TypeRef.IsMap || field.JsonField {
		return goMember{}, false
	}
	required := field.Required && !field.AutoGenerated
	name := field.TypeRef.Name
	switch name {
	case codegen.PrimitiveString, codegen.PrimitiveNumber:
		return goMember{}, true
	case codegen.PrimitiveBoolean:
		return goMember{pointer: codegen.GoOptionalBoolIsPointer(name, required, false, false, field.Default)}, true
	}
	if def, ok := types.findScalarDef(name); ok {
		if def.StructuredJSONType() != "" || def.IsAnyJSON() {
			return goMember{}, false
		}
		traits := codegen.BuildScalarTraits(def, codegen.BuildScalarTokens(name), def.TypeMappings["go"])
		pointer := !required
		if traits.IsIntegerLike && !field.DistinctNull && !codegen.GoOptionalIntegerScalarIsPointer(required, false, false, field.Default) {
			pointer = false
		}
		return goMember{pointer: pointer}, true
	}
	if types.isEnum(name) {
		return goMember{pointer: !required, enum: name}, true
	}
	return goMember{}, false
}

// unservedError is why the Go server writes no handler for a table
// operation yet (D106): the loader filled it, and what it reads or writes
// is past what tables.go copies member by member. The operation then stays
// one the project implements, and the build warns of it
// (UnservedTableOperations). Any other error of the model is the
// generator's.
type unservedError struct{ reason string }

func (e *unservedError) Error() string { return e.reason }

// unserved is an unservedError of the reason, worded to follow the
// operation's name: "its view IssueView holds labels, string[], ...".
func unserved(format string, args ...any) error {
	return &unservedError{reason: fmt.Sprintf(format, args...)}
}

// unservable is why a view's field keeps its operation from a handler: a
// value the Go handlers do not copy.
func unservable(_ string, view *tableView, field *ir.FieldDef) error {
	return unserved("its view %s holds %s, %s, and the Go server writes handlers for a view of single values so far, each a scalar, text, a number, a boolean or an enum", view.def.Name, field.Name, typeLabel(field.TypeRef))
}

// typeLabel words a type reference for a message: string[], a map of
// Money.
func typeLabel(ref ir.TypeRef) string {
	switch {
	case ref.IsMap:
		return "a map of " + ref.Name
	case ref.IsArrayOfArrays:
		return ref.Name + "[][]"
	case ref.IsArray:
		return ref.Name + "[]"
	}
	return "a value of " + ref.Name
}

// rowMember is how a row holds the value of a column: the expression of
// the member, how Go holds it, and, for the key of a relation that may be
// absent, the relation's member, a pointer that is nil without one.
type rowMember struct {
	expr     string
	held     goMember
	relation string
}

// rowMemberOf is how row, a Go expression of a row of view's table, holds
// the value of column.
func (b *tableBuilder) rowMemberOf(owner string, view *tableView, field *ir.FieldDef, column *sqlutil.ColumnModel, row string) (rowMember, error) {
	name := codegen.ToPascalCase(column.Field)
	if column.Relation == "" {
		if column.Def == nil {
			if column.Key {
				// A key the generator adds: a UUID the database generates.
				return rowMember{expr: row + "." + name, held: goMember{pointer: true}}, nil
			}
			return rowMember{}, unservable(owner, view, field)
		}
		held, ok := goMemberOf(b.dbTypes, column.Def)
		if !ok {
			return rowMember{}, unservable(owner, view, field)
		}
		return rowMember{expr: row + "." + name, held: held}, nil
	}
	target := b.tables[column.Relation]
	if target == nil || target.KeyColumn() == nil {
		return rowMember{}, fmt.Errorf("apigen: operation %s: its view %s holds %s, the key of %s, which is no table of %s with a repository (D106)", owner, view.def.Name, field.Name, column.Relation, b.dbName)
	}
	key := target.KeyColumn()
	held := goMember{pointer: true}
	if key.Def != nil {
		var ok bool
		if held, ok = goMemberOf(b.dbTypes, key.Def); !ok {
			return rowMember{}, unservable(owner, view, field)
		}
	}
	out := rowMember{expr: row + "." + name + "." + codegen.ToPascalCase(key.Field), held: held}
	if !column.Required {
		out.relation = row + "." + name
	}
	return out, nil
}

// uuidScalar is the scalar a principal's id reads as (orm.ParseUserID).
const uuidScalar = "Identity.UUID"

// rowScalar names the scalar a row holds in column: the column's own, or,
// for a to-one relation's key, the key's of the row it points at, a UUID
// for one the generator adds.
func rowScalar(tables map[string]*sqlutil.TableModel, column *sqlutil.ColumnModel) string {
	def := column.Def
	if column.Relation != "" {
		def = nil
		if target := tables[column.Relation]; target != nil && target.KeyColumn() != nil {
			def = target.KeyColumn().Def
		}
	}
	if def == nil {
		return uuidScalar
	}
	return def.TypeRef.Name
}

// assign are the statements that set dst from src, each a Go expression
// and how Go holds it: a pointer's value is read when it is not nil and
// set through a variable, and an enum's goes through convert, its Go type
// in the package dst is of.
func assign(dst string, to goMember, src string, from goMember, convert string) []string {
	value := func(expr string) string {
		if convert == "" {
			return expr
		}
		return convert + "(" + expr + ")"
	}
	switch {
	case !from.pointer && !to.pointer:
		return []string{dst + " = " + value(src)}
	case from.pointer && !to.pointer:
		return []string{"if " + src + " != nil {", dst + " = " + value("*"+src), "}"}
	case !from.pointer && to.pointer:
		return []string{"{", "value := " + value(src), dst + " = &value", "}"}
	case convert == "":
		return []string{dst + " = " + src}
	}
	return []string{"if " + src + " != nil {", "value := " + value("*"+src), dst + " = &value", "}"}
}

// unbraced is lines without the block assign opens for a variable, when
// the statements around them are a block of their own already.
func unbraced(lines []string, scoped bool) []string {
	if scoped && len(lines) > 2 && lines[0] == "{" && lines[len(lines)-1] == "}" {
		return lines[1 : len(lines)-1]
	}
	return lines
}

// view models the function that fills view from a row of its table, once
// per view, and returns its name.
func (b *tableBuilder) view(owner string, view *tableView) (string, error) {
	name := "as" + view.def.Name
	if b.views[view.def.Name] {
		return name, nil
	}
	fn := TableView{Func: name, View: view.def.Name, Table: view.model.Type, RowType: b.alias + "." + view.model.Type}
	for _, field := range view.def.Fields {
		if field == nil {
			continue
		}
		column := view.column(field.Name)
		if column == nil || field.Virtual {
			return "", fmt.Errorf("apigen: operation %s: its result %s holds %s, which no column of %s holds; the loader holds a @tableOperation's view to its table (D106)", owner, view.def.Name, field.Name, view.model.Type)
		}
		to, ok := goMemberOf(b.source.types, field)
		if !ok {
			return "", unservable(owner, view, field)
		}
		from, err := b.rowMemberOf(owner, view, field, column, "row")
		if err != nil {
			return "", err
		}
		if (to.enum == "") != (from.held.enum == "") {
			return "", fmt.Errorf("apigen: operation %s: its result %s holds %s as %s, and %s holds %s as another kind of value (D106)", owner, view.def.Name, field.Name, typeLabel(field.TypeRef), view.model.Type, column.Field)
		}
		convert := ""
		if to.enum != "" {
			convert = "types." + to.enum
		}
		lines := assign("view."+codegen.ToPascalCase(field.Name), to, from.expr, from.held, convert)
		if from.relation != "" {
			lines = append(append([]string{"if " + from.relation + " != nil {"}, lines...), "}")
		}
		fn.Lines = append(fn.Lines, lines...)
	}
	b.views[view.def.Name] = true
	b.info.Views = append(b.info.Views, fn)
	return name, nil
}

// tableRows models what the refusals of the writes of view's table name,
// once per table, and returns the name of its variable.
func (b *tableBuilder) tableRows(owner string, view *tableView) (string, error) {
	name := "rowsOf" + view.model.Type
	if b.rows[view.model.Type] {
		return name, nil
	}
	rows := TableRows{Var: name, Type: view.model.Type}
	for i := range view.model.Columns {
		c := &view.model.Columns[i]
		if c.Internal || c.Audit != "" || c.Def == nil {
			continue
		}
		rows.Columns = append(rows.Columns, TableColumn{Column: c.Name, Member: member(c), Related: c.Relation})
	}
	b.rows[view.model.Type] = true
	b.info.Rows = append(b.info.Rows, rows)
	return name, nil
}

// rowKey is the key of the row a handler found, as the repository's
// UpdateOne and DeleteOne take it.
func (b *tableBuilder) rowKey(owner string, view *tableView) (string, error) {
	key := view.model.KeyColumn()
	if key == nil {
		return "", fmt.Errorf("apigen: operation %s: %s has no key (D106)", owner, view.model.Type)
	}
	expr := "row." + codegen.ToPascalCase(key.Field)
	pointer := true
	if key.Def != nil {
		held, ok := goMemberOf(b.dbTypes, key.Def)
		if !ok {
			return "", fmt.Errorf("apigen: operation %s: the key %s of %s is no single scalar, text or number (D106)", owner, key.Field, view.model.Type)
		}
		pointer = held.pointer
	}
	if pointer {
		return "*" + expr, nil
	}
	return expr, nil
}

// list models the body of a list: the repository's FindPage under the
// path's parents, the request's filter member for member, its search text,
// its sort and its cursor, the repository's page key.
func (b *tableBuilder) list(owner string, endpoint EndpointInfo, h *TableHandler, result *tableView, to *ir.TableOperation, conditions []string, asView, repo string, params *[]string) error {
	if endpoint.Page == nil || endpoint.HasInput || len(endpoint.ScalarArgs) > 0 || !endpoint.OutputIsArray {
		return fmt.Errorf("apigen: operation %s: a generated list is a @paginated GET of its path's parameters, a SortBy, a SearchQuery and a FilterBy; the loader holds a @tableOperation to it (D106)", owner)
	}
	b.info.UsesPaging = true
	typeName := to.Type
	h.Result = "paging.Page[types." + endpoint.OutputType + "]"
	h.Zero = h.Result + "{}"
	body := []string{"where := &orm." + typeName + "Filter{" + strings.Join(conditions, ", ") + "}"}

	// The arguments beside the path's: the search text, and the sort,
	// which the page holds.
	for _, param := range endpoint.ArgQueryParams() {
		if !param.Search {
			return fmt.Errorf("apigen: operation %s: argument %s is a query parameter, and nothing says what a generated list compares it with; the loader refuses one on a @tableOperation (D106)", owner, param.Name)
		}
		if !result.model.Search {
			return fmt.Errorf("apigen: operation %s: argument %s is a SearchQuery, and %s has no search field (D106)", owner, param.Name, typeName)
		}
		if param.IsArray || param.IsInt || param.IsFloat || param.IsBool || param.IsUUID || param.IsDateTime {
			return fmt.Errorf("apigen: operation %s: its search argument %s is no text (D106)", owner, param.Name)
		}
		if param.Required {
			*params = append(*params, param.GoName+" "+param.GoType)
			b.info.UsesTexts = true
			body = append(body, "where.SearchText = tableText(&"+param.GoName+")")
		} else {
			*params = append(*params, param.GoName+" *"+param.GoType)
			b.info.UsesTexts = true
			body = append(body, "where.SearchText = tableText("+param.GoName+")")
		}
	}

	if filter := endpoint.Filter; filter != nil {
		*params = append(*params, "filters "+endpoint.GoFilterTypeName())
		held := map[string]bool{}
		for _, parent := range to.Parents {
			held[parent.Param] = true
		}
		for _, field := range filter.Fields {
			lines, err := b.filterMember(owner, result, field, held[field.Name])
			if err != nil {
				return err
			}
			body = append(body, lines...)
		}
	}

	// A row rule (D108) holds beside the parents and whatever the filter
	// says: a caller no row can name lists none.
	reach, err := b.reach(owner, result, to, []string{"result.Items = []types." + endpoint.OutputType + "{}", "return nil"})
	if err != nil {
		return err
	}
	body = append(body, reach...)

	*params = append(*params, "page paging.Request")
	options := "Fields: orm." + typeName + "Fields{}.All(), Limit: page.Limit"
	if sort := endpoint.Page.Sort; sort != nil {
		for _, field := range sort.Fields {
			column := result.column(field)
			if column == nil || column.Relation != "" || !column.Sortable {
				return fmt.Errorf("apigen: operation %s: its sort names %s, a column the repository of %s does not page by (D106)", owner, field, typeName)
			}
		}
		options += ", Sort: orm." + typeName + "Sort{Field: orm." + typeName + "SortField(page.Sort.Field), Desc: page.Sort.Desc}"
	}
	body = append(body,
		"options := &orm."+typeName+"PageOptions{"+options+"}",
		"if page.Cursor != \"\" {",
		"options.After = &orm."+typeName+"PageKey{}",
		"if err := paging.DecodeCursor(page.Cursor, options.After); err != nil {",
		"return err",
		"}",
		"}",
		"rows, err := "+repo+".FindPage(ctx, where, options)",
		"if err != nil {",
		"return err",
		"}",
		"result.Items = make([]types."+endpoint.OutputType+", len(rows.Rows))",
		"for i, row := range rows.Rows {",
		"result.Items[i] = "+asView+"(row)",
		"}",
		"if rows.Next != nil {",
		"next, err := paging.EncodeCursor(rows.Next)",
		"if err != nil {",
		"return err",
		"}",
		"result.Next = &next",
		"}",
		"return nil",
	)
	h.Body = body
	return nil
}

// filterMember are the statements that turn one member of the request's
// filter into the repository's: each operator the request sent as the
// repository's of the same name, ne and notIn among them (D112), contains
// as its ilike of the value with its \, % and _ escaped. A member the path's parent holds too, parent,
// is ANDed with it.
func (b *tableBuilder) filterMember(owner string, view *tableView, field FilterFieldInfo, parent bool) ([]string, error) {
	column := view.column(field.Name)
	if column == nil {
		return nil, fmt.Errorf("apigen: operation %s: its filter names %s, which no column of %s holds (D106)", owner, field.Name, view.model.Type)
	}
	target := codegen.ToPascalCase(column.Field)
	if column.Relation != "" {
		target += "ID"
	}
	goType := field.Value.GoType
	text := func(expr string, list bool) string {
		b.info.UsesTexts = true
		if list {
			return "tableTexts(" + expr + ")"
		}
		return "tableText(" + expr + ")"
	}
	same := func(expr string, _ bool) string { return expr }
	var filterType string
	convert := same
	switch column.Filter {
	case sqlutil.FilterUUID:
		filterType = "UUIDFilter"
		if !field.Value.IsUUID {
			return nil, b.unfiltered(owner, view, field, column)
		}
	case sqlutil.FilterString:
		filterType, convert = "StringFilter", text
		if field.Value.IsInt || field.Value.IsFloat || field.Value.IsBool || field.Value.IsUUID || field.Value.IsDateTime {
			return nil, b.unfiltered(owner, view, field, column)
		}
	case sqlutil.FilterDate:
		filterType, convert = "DateFilter", text
	case sqlutil.FilterDecimal:
		filterType, convert = "DecimalFilter", text
	case sqlutil.FilterInt:
		filterType = "Int64Filter"
		if goType != "int64" {
			return nil, b.unfiltered(owner, view, field, column)
		}
	case sqlutil.FilterFloat:
		filterType = "FloatFilter"
		if goType != "float64" {
			return nil, b.unfiltered(owner, view, field, column)
		}
	case sqlutil.FilterBool:
		filterType = "BoolFilter"
		if goType != "bool" {
			return nil, b.unfiltered(owner, view, field, column)
		}
	case sqlutil.FilterDateTime:
		filterType = "DateTimeFilter"
		if goType != "types.TemporalDateTime" {
			return nil, b.unfiltered(owner, view, field, column)
		}
		b.info.UsesTime = true
		convert = func(expr string, list bool) string {
			if list {
				return "tableTimes(" + expr + ")"
			}
			return "tableTime(" + expr + ")"
		}
	default:
		return nil, b.unfiltered(owner, view, field, column)
	}
	held := map[string]bool{}
	for _, op := range sqlutil.FilterOps(column.Filter) {
		held[op] = true
	}
	var members []string
	for _, op := range field.Operators {
		switch op {
		case ir.FilterOpEq, ir.FilterOpNe, ir.FilterOpGt, ir.FilterOpGte, ir.FilterOpLt, ir.FilterOpLte:
			if !held[op] {
				return nil, b.unfiltered(owner, view, field, column)
			}
			name := codegen.ToPascalCase(op)
			members = append(members, name+": "+convert("filter."+name, false))
		case ir.FilterOpIn, ir.FilterOpNotIn:
			if !held[op] {
				return nil, b.unfiltered(owner, view, field, column)
			}
			name := codegen.ToPascalCase(op)
			members = append(members, name+": "+convert("filter."+name, true))
		case ir.FilterOpContains:
			if !held[sqlutil.FilterILike] || goType != "string" {
				return nil, b.unfiltered(owner, view, field, column)
			}
			b.info.UsesContains = true
			members = append(members, "ILike: tableContains(filter.Contains)")
		case ir.FilterOpIsNull:
			if !held[sqlutil.FilterIsNull] {
				return nil, b.unfiltered(owner, view, field, column)
			}
			members = append(members, "IsNull: filter.IsNull")
		default:
			return nil, b.unfiltered(owner, view, field, column)
		}
	}
	condition := "&orm." + filterType + "{" + strings.Join(members, ", ") + "}"
	set := "where." + target + " = " + condition
	if parent {
		// The path's parent holds the member already: the request's
		// filter on it holds beside it, not in its place.
		set = "where.AllOf = append(where.AllOf, orm." + view.model.Type + "Filter{" + target + ": " + condition + "})"
	}
	return []string{"if filter := filters." + field.GoName + "; filter != nil {", set, "}"}, nil
}

// unfiltered is the error of a filter's field the Go repositories keep no
// such filter for.
func (b *tableBuilder) unfiltered(_ string, view *tableView, field FilterFieldInfo, column *sqlutil.ColumnModel) error {
	return unserved("its filter names %s, %s with %s, and the Go repository of %s keeps no such filter on %s", field.Name, field.Kind, strings.Join(field.Operators, ", "), view.model.Type, column.Field)
}

// bodyMember is one member of a create's or an update's body held to its
// column: how the view holds it, how the body's Go type does, and the
// enum's Go type in the database's types package, for an enum.
type bodyMember struct {
	field   *ir.FieldDef
	column  *sqlutil.ColumnModel
	held    goMember
	input   string
	convert string
}

// bodyMembers are the members of body, a create's or an update's, in the
// view's order.
func (b *tableBuilder) bodyMembers(owner string, body *tableView) ([]bodyMember, error) {
	var out []bodyMember
	for _, field := range body.def.Fields {
		if field == nil {
			continue
		}
		column := body.column(field.Name)
		if column == nil || field.Virtual || column.Internal || column.Audit != "" || (column.Def != nil && column.Def.Owned()) {
			return nil, fmt.Errorf("apigen: operation %s: its body %s sets %s, which is no column of %s a caller may set; the loader holds a @tableOperation's body to its table (D106)", owner, body.def.Name, field.Name, body.model.Type)
		}
		held, ok := goMemberOf(b.source.types, field)
		if !ok {
			return nil, unservable(owner, body, field)
		}
		m := bodyMember{field: field, column: column, held: held, input: "input." + codegen.ToPascalCase(field.Name)}
		if held.enum != "" {
			if column.Def == nil || !b.dbTypes.isEnum(column.Def.TypeRef.Name) {
				return nil, fmt.Errorf("apigen: operation %s: its body %s holds %s as the enum %s, and %s holds %s as another kind of value (D106)", owner, body.def.Name, field.Name, held.enum, body.model.Type, column.Field)
			}
			m.convert = b.alias + "." + column.Def.TypeRef.Name
		}
		out = append(out, m)
	}
	return out, nil
}

// set is the Go condition under which the request sets a member of its
// body to a value: a pointer that is not nil, and for a value Go holds as
// its zero the body's own JSON, which tells a member left out and a null
// from a zero. "" for a member the body requires.
func (b *tableBuilder) set(m bodyMember) string {
	switch {
	case m.field.Required:
		return ""
	case m.held.pointer:
		return m.input + " != nil"
	}
	b.info.UsesMembers = true
	return "members.set(" + strconv.Quote(m.field.Name) + ")"
}

// defaultOf is the Go literal of the default of column, a column a row
// requires, as the row holds it: the ORM's CreateOne writes the row's
// value of such a column, so a create that leaves it unsaid writes the
// schema's default itself.
func (b *tableBuilder) defaultOf(table *sqlutil.TableModel, c *sqlutil.ColumnModel) (string, error) {
	raw := *c.Default
	name := c.Def.TypeRef.Name
	trimmed := strings.TrimSpace(raw)
	switch {
	case b.dbTypes.isEnum(name):
		return b.alias + "." + name + "(" + strconv.Quote(raw) + ")", nil
	case name == codegen.PrimitiveBoolean || c.Class.BoolLike:
		if lower := strings.ToLower(trimmed); lower == "true" || lower == "false" {
			return lower, nil
		}
	case c.Class.IntLike:
		if _, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return trimmed, nil
		}
	case name == codegen.PrimitiveNumber || c.Class.FloatLike:
		if _, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return trimmed, nil
		}
	case name == codegen.PrimitiveString || ((c.Class.StringLike || c.Class.Decimal || c.Class.DateLike) && !c.Class.UUIDLike && !c.Class.DateTimeLike):
		return strconv.Quote(raw), nil
	}
	return "", unserved("a create of %s leaves %s to its default, %s, which the Go server cannot write as a value of %s", table.Type, c.Field, raw, name)
}

// create models the body of a create: a row of the path's parents, the
// members the body sets and the defaults of the columns it leaves unsaid,
// through the repository's CreateOne.
func (b *tableBuilder) create(owner string, h *TableHandler, body *tableView, parents map[string]*sqlutil.ColumnModel, endpoint EndpointInfo, asView string, operation *ir.TableOperation) error {
	members, err := b.bodyMembers(owner, body)
	if err != nil {
		return err
	}
	typeName := body.model.Type
	// A table that composes behaviors is created from <Type>Create, which
	// leaves the fields its behaviors own to the core (D103).
	if len(body.table.TableBehaviors()) > 0 {
		h.Body = append(h.Body, "row := &orm."+typeName+"Create{}")
	} else {
		h.Body = append(h.Body, "row := &"+b.alias+"."+typeName+"{}")
	}

	// to is the row's member a write sets for column, and the statements
	// that make it settable: a relation that may be absent is a pointer.
	to := func(field *ir.FieldDef, column *sqlutil.ColumnModel) (rowMember, []string, error) {
		target, err := b.rowMemberOf(owner, body, field, column, "row")
		if err != nil {
			return rowMember{}, nil, err
		}
		var before []string
		if target.relation != "" {
			before = []string{target.relation + " = &" + b.alias + "." + column.Relation + "{}"}
		}
		return target, before, nil
	}

	byMember := map[string]bodyMember{}
	for _, m := range members {
		byMember[m.field.Name] = m
	}
	for i := range body.model.Columns {
		c := &body.model.Columns[i]
		name := member(c)
		_, inBody := byMember[name]
		_, isParent := parents[name]
		if c.Def == nil || c.Internal || c.Audit != "" || c.Def.Owned() || c.Relation != "" || c.Key || isParent {
			continue
		}
		if !c.Required || c.Default == nil || c.DefaultsToNow() {
			continue
		}
		if inBody && byMember[name].field.Required {
			continue
		}
		// A column the row requires and the request may leave unsaid:
		// the schema's default, which a member the body sets replaces.
		literal, err := b.defaultOf(body.model, c)
		if err != nil {
			return err
		}
		h.Body = append(h.Body, "row."+codegen.ToPascalCase(c.Field)+" = "+literal)
	}

	for _, param := range endpoint.PathParams {
		column, ok := parents[param.Name]
		if !ok {
			continue
		}
		field := &ir.FieldDef{Name: param.Name}
		target, before, err := to(field, column)
		if err != nil {
			return err
		}
		h.Body = append(h.Body, before...)
		h.Body = append(h.Body, assign(target.expr, target.held, param.GoName, goMember{}, "")...)
	}

	for _, m := range members {
		if _, isParent := parents[m.field.Name]; isParent {
			return fmt.Errorf("apigen: operation %s: its body %s sets %s, the relation the path's {%s} sets; the loader refuses it on a @tableOperation (D106)", owner, body.def.Name, m.field.Name, m.field.Name)
		}
		target, before, err := to(m.field, m.column)
		if err != nil {
			return err
		}
		if (m.held.enum == "") != (target.held.enum == "") {
			return fmt.Errorf("apigen: operation %s: its body %s holds %s as %s, and %s holds %s as another kind of value (D106)", owner, body.def.Name, m.field.Name, typeLabel(m.field.TypeRef), typeName, m.column.Field)
		}
		// Under its condition a pointer is not nil: its value is read.
		condition := b.set(m)
		source, held := m.input, m.held
		if condition != "" && held.pointer && (m.convert != "" || !target.held.pointer) {
			source, held = "*"+m.input, goMember{enum: held.enum}
		}
		lines := append(before, unbraced(assign(target.expr, target.held, source, held, m.convert), condition != "")...)
		if condition != "" {
			lines = append(append([]string{"if " + condition + " {"}, lines...), "}")
		}
		h.Body = append(h.Body, lines...)
	}
	// The row rule's members name the caller in the new row (D108):
	// createdBy as the repository stamps it, each other one set here,
	// after the body's, which the loader holds from setting one.
	var callers []string
	for _, name := range operation.By {
		column := body.column(name)
		if column == nil {
			return fmt.Errorf("apigen: operation %s: its row rule names %s, which is no member of %s; the loader holds a @tableOperation's rule to its table (D108)", owner, name, typeName)
		}
		if column.Audit != "" {
			continue
		}
		if _, set := byMember[name]; set {
			return fmt.Errorf("apigen: operation %s: its body %s sets %s, the member its row rule sets to the caller; the loader refuses it on a @tableOperation (D108)", owner, body.def.Name, name)
		}
		if column.Filter != sqlutil.FilterUUID {
			return unserved("its row rule names %s, which holds text, and the Go server names the caller by a UUID alone so far (D108)", name)
		}
		if held := rowScalar(b.tables, column); held != uuidScalar {
			return unserved("its row rule names %s, which holds %s, and a create of the Go server sets the caller as %s alone so far (D108)", name, held, uuidScalar)
		}
		target, before, err := to(&ir.FieldDef{Name: name}, column)
		if err != nil {
			return err
		}
		callers = append(callers, before...)
		callers = append(callers, assign(target.expr, target.held, "caller", goMember{}, "")...)
	}
	if len(callers) > 0 {
		b.info.UsesRowRules, b.info.UsesRowCreate = true, true
		h.Body = append(h.Body,
			"caller, named := tableCallerID(ctx)",
			"if !named {",
			"return tableUnnamedCaller(ctx, "+strconv.Quote(typeName)+")",
			"}")
		h.Body = append(h.Body, callers...)
	}
	refusals := b.refusals(nil, members, false)
	if len(refusals) > 0 || slices.ContainsFunc(h.Body, func(line string) bool { return strings.Contains(line, "members.set(") }) {
		b.info.UsesMembers = true
		h.Before = append(append(h.Before, tableMembersLines...), refusals...)
	}
	h.Body = append(h.Body,
		"created, err := repo.CreateOne(ctx, row)",
		"if err != nil {",
		"return err",
		"}",
		"view := "+asView+"(created)", "result = &view", "return nil")
	return nil
}

// refusals are the statements that refuse a body at its members, with the
// 400 a field validation error of the input gets, before a create's
// transaction and, in an update's, once the row is found, so a row that
// does not exist is not found whatever the body holds: a null at one of
// required, the quoted names of the members whose
// column may not be null, and a zero the body holds for a member Go
// declares as a value, text or a number that may be absent, when the
// member's rules refuse it. The generated type's Validate reads such a
// zero as a member left out, and checks nothing of it; the body's own
// JSON says it is there.
func (b *tableBuilder) refusals(required []string, members []bodyMember, inTransaction bool) []string {
	var checks []string
	if len(required) > 0 {
		checks = append(checks,
			"for _, name := range []string{"+strings.Join(required, ", ")+"} {",
			"if members.null(name) {",
			`refusal.AddFieldError(name, "required", "required field")`,
			"}",
			"}")
	}
	for _, m := range members {
		if m.field.Required || m.held.pointer || m.held.enum != "" {
			continue
		}
		name := strconv.Quote(m.field.Name)
		refuse := func(zero, validator, message string) {
			checks = append(checks,
				"if members.set("+name+") && "+m.input+" == "+zero+" {",
				"refusal.AddFieldError("+name+", "+strconv.Quote(validator)+", "+strconv.Quote(message)+")",
				"}")
		}
		switch m.field.TypeRef.Name {
		case codegen.PrimitiveString:
			if min := m.field.ValidateMinLength; min != nil && *min > 0 {
				refuse(`""`, "minLength", fmt.Sprintf("must be at least %d characters", *min))
			}
			if pattern, err := regexp.Compile(m.field.ValidatePattern); m.field.ValidatePattern != "" && err == nil && !pattern.MatchString("") {
				refuse(`""`, "pattern", "invalid format")
			}
		case codegen.PrimitiveNumber:
			if min := m.field.ValidateMin; min != nil && *min > 0 {
				refuse("0", "min", fmt.Sprintf("must be at least %g", *min))
			}
			if max := m.field.ValidateMax; max != nil && *max < 0 {
				refuse("0", "max", fmt.Sprintf("must be at most %g", *max))
			}
		}
	}
	if len(checks) == 0 {
		return nil
	}
	refused := "return nil, &tableRefusal{errors: refusal}"
	if inTransaction {
		refused = "return &tableRefusal{errors: refusal}"
	}
	lines := append([]string{"refusal := types.NewValidationErrors()"}, checks...)
	return append(lines, "if refusal.HasErrors() {", refused, "}")
}

// tableMembersLines read the members of a body from its own JSON.
var tableMembersLines = []string{
	"members, err := tableMembersOf(raw)",
	"if err != nil {",
	"return nil, err",
	"}",
}

// update models the body of an update: the members the request's JSON
// holds, a value set and a null clearing a column that may be null, through
// the repository's UpdateOne, which a body of no member does not call. A
// null for a column that may not be null is refused at its member, once
// the row is found.
func (b *tableBuilder) update(owner string, h *TableHandler, body *tableView, rowKey, asView string) error {
	members, err := b.bodyMembers(owner, body)
	if err != nil {
		return err
	}
	typeName := body.model.Type
	b.info.UsesMembers = true
	h.Before = append(h.Before, tableMembersLines...)
	var required []string
	lines := []string{"update := &orm." + typeName + "Update{}", "changed := false"}
	for _, m := range members {
		if m.field.Required {
			return fmt.Errorf("apigen: operation %s: its body %s requires %s; an update's members are all optional, which the loader holds a @tableOperation to (D106)", owner, body.def.Name, m.field.Name)
		}
		if m.column.Key {
			return fmt.Errorf("apigen: operation %s: its body %s sets %s, the key of %s, which never changes (D106)", owner, body.def.Name, m.field.Name, typeName)
		}
		target := "update." + codegen.ToPascalCase(m.column.Field)
		if m.column.Relation != "" {
			target += "ID"
		} else if (m.held.enum == "") != (m.column.Def == nil || !b.dbTypes.isEnum(m.column.Def.TypeRef.Name)) {
			return fmt.Errorf("apigen: operation %s: its body %s holds %s as %s, and %s holds %s as another kind of value (D106)", owner, body.def.Name, m.field.Name, typeLabel(m.field.TypeRef), typeName, m.column.Field)
		}
		if _, ok := goMemberOf(b.dbTypes, m.column.Def); !ok && m.column.Relation == "" {
			return unservable(owner, body, m.field)
		}
		name := strconv.Quote(m.field.Name)
		value := assign(target, goMember{pointer: true}, m.input, m.held, m.convert)
		// An update's member is a pointer to the value, whatever the
		// body's Go type holds: a pointer's value is copied.
		if m.held.pointer && m.convert == "" {
			value = []string{"if " + m.input + " != nil {", "value := *" + m.input, target + " = &value", "}"}
		}
		value = unbraced(value, true)
		if m.column.Required {
			required = append(required, name)
			lines = append(lines, "if members.set("+name+") {")
			lines = append(lines, value...)
			lines = append(lines, "changed = true", "}")
			continue
		}
		lines = append(lines, "switch {", "case members.null("+name+"):", target+"SetNull = true", "changed = true", "case members.set("+name+"):")
		lines = append(lines, value...)
		lines = append(lines, "changed = true", "}")
	}
	h.Body = append(h.Body, b.refusals(required, members, true)...)
	h.Body = append(h.Body, lines...)
	h.Body = append(h.Body,
		"if changed {",
		"if row, err = repo.UpdateOne(ctx, "+rowKey+", update); err != nil {",
		"return err",
		"}",
		"}",
		"view := "+asView+"(row)", "result = &view", "return nil")
	return nil
}

// TableRequiresCaller reports whether the handler factory of a table
// operation's route checks for a caller itself: the route requires one
// (@auth), names no permission, whose check answers a request without one,
// and no service clause, and the API's Config takes no auth middleware to
// have put one on the request (AuthWired). A project that mounts
// RegisterRoutes behind a middleware of its own is then answered 401 for a
// request it let through with no caller, where an operation it wrote would
// run its implementation.
func (r routeData) TableRequiresCaller() bool {
	return r.ServedTable() != nil && r.Output != nil && !r.Output.AuthWired() &&
		r.RequiresAuth && len(r.RequiredPerms) == 0 && r.ServiceCallers == nil
}
