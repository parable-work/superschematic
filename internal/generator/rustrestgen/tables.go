package rustrestgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The table operations of a Rust API (D106, D116). An operation marked
// @tableOperation has no method in its namespace's trait: the router's
// handler hands what it decoded to src/tables.rs, which serves it from the
// table's repository in the database's Rust types (the HTTP runtime's
// orm::routes), in one transaction, as the request's caller. The
// operation is carried as data, the runtime's TableOperation, with the
// members of the TypeScript router's.

// TablesFile is the file of an API crate that serves its table operations.
const TablesFile = "tables.rs"

// TableInfo is a table operation as the crate serves it.
type TableInfo struct {
	// Operation names the operation as the loader does, Set.operation.
	Operation string
	// Kind, Database and Type are the loader's: what the operation does,
	// the DB service, and the table.
	Kind     string
	Database string
	Type     string

	// Literal is the operation as src/tables.rs holds it, the runtime's
	// TableOperation in JSON, as a Rust raw string literal.
	Literal string

	// Path are the path parameters, each by the row's member it names and
	// the Args' field that holds it.
	Path []ParamInfo
	// Search is the Args' field of a list's SearchQuery argument, and
	// SearchOptional whether it is an Option; empty without one.
	Search         string
	SearchOptional bool
	// Input is the Args' field of a create's and an update's body.
	Input string
	// Filter are the fields of a list's filter, and FilterField the Args'
	// field that holds them.
	Filter      []FilterFieldInfo
	FilterField string
}

// List reports whether the operation lists rows, and answers a page.
func (t *TableInfo) List() bool {
	return t.Kind == ir.TableOperationList
}

// Article is the operation's kind with its article: a read, an update.
func (t *TableInfo) Article() string {
	if t.Kind == ir.TableOperationUpdate {
		return "an " + t.Kind
	}
	return "a " + t.Kind
}

// tableOperation is the runtime's TableOperation, the members the
// TypeScript router's literal has.
type tableOperation struct {
	Kind               string   `json:"kind"`
	Type               string   `json:"type"`
	Key                string   `json:"key,omitempty"`
	Parents            []string `json:"parents,omitempty"`
	View               []string `json:"view"`
	Body               []string `json:"body,omitempty"`
	Search             string   `json:"search,omitempty"`
	Starts             []string `json:"starts,omitempty"`
	By                 []string `json:"by,omitempty"`
	OverridePermission string   `json:"overridePermission,omitempty"`
}

// UnservedTable is a table operation the Rust server writes no handler for
// yet (D106, D116), and why: it stays a method of its namespace's trait,
// to be written by hand, and the build warns of it.
type UnservedTable struct {
	Operation string
	Reason    string
}

// tableInfo is the table operation endpoint serves, or nil for any other
// operation, and for one the Rust server writes no handler for yet, with
// why: an operation over a projection. The loader
// fills an operation's TableOperation and refuses what no handler serves;
// IR that did not go through it is refused here, naming the operation,
// since a handler written from half of it would serve something else.
func tableInfo(schema *ir.Schema, endpoint apigen.EndpointInfo, info EndpointInfo) (*TableInfo, string, error) {
	op := endpoint.TableOperation()
	if op == nil {
		return nil, "", nil
	}
	owner := info.Namespace + "." + info.Name
	refuse := func(format string, args ...any) (*TableInfo, string, error) {
		return nil, "", fmt.Errorf("rustrestgen: %s is a @tableOperation (D106) %s", owner, fmt.Sprintf(format, args...))
	}
	unserved := func(format string, args ...any) (*TableInfo, string, error) {
		return nil, fmt.Sprintf(format, args...), nil
	}
	if !slices.Contains(ir.TableOperationKinds, op.Kind) || op.Type == "" || op.Database == "" {
		return refuse("whose kind, table and database are not all set (kind %q, type %q, database %q); the loader fills an operation's tableOperation and checks it, so load the schema before generating", op.Kind, op.Type, op.Database)
	}
	if op.Projection {
		return unserved("it reads the projection %s, and the Rust repositories read tables so far", op.Type)
	}
	table := &TableInfo{Operation: owner, Kind: op.Kind, Database: op.Database, Type: op.Type}
	// A create that names the state its row starts in (D109) carries the
	// members that name it: the runtime's handler creates the row and
	// makes each Workflow's own transition, through the behaviors' core.
	data := tableOperation{Kind: op.Kind, Type: op.Type, Key: op.Key, Starts: op.Starts, By: op.By, OverridePermission: op.OverridePermission}

	pathArg := func(name string) *ParamInfo {
		for i := range info.PathArgs {
			if info.PathArgs[i].Name == name {
				return &info.PathArgs[i]
			}
		}
		return nil
	}
	keyed := op.Kind != ir.TableOperationList && op.Kind != ir.TableOperationCreate
	if keyed {
		arg := pathArg(op.Key)
		if op.Key == "" || arg == nil {
			return refuse("whose route, %s %s, has no path parameter for the key of %s (%q)", info.Method, info.Path, op.Type, op.Key)
		}
		table.Path = append(table.Path, *arg)
	}
	for _, parent := range op.Parents {
		arg := pathArg(parent.Param)
		if parent.Param == "" || arg == nil {
			return refuse("whose route, %s %s, has no path parameter %q, a parent's", info.Method, info.Path, parent.Param)
		}
		data.Parents = append(data.Parents, parent.Param)
		table.Path = append(table.Path, *arg)
	}
	if len(info.PathArgs) != len(table.Path) {
		return refuse("whose route, %s %s, has a path parameter that is neither its key nor a parent", info.Method, info.Path)
	}

	view := schema.Types[endpoint.OutputType]
	if view == nil || view.Source == nil || len(view.Fields) == 0 {
		return refuse("whose result, %s, is no @source view with fields", endpoint.OutputType)
	}
	for _, field := range view.Fields {
		if field == nil {
			continue
		}
		if field.Virtual {
			return refuse("whose result %s has a @virtual field, %s, which no implementation fills", view.Name, field.Name)
		}
		// A @decrypted member is the opened value of its Encrypted<T>
		// column (D85), which the repository's row holds: the repositories
		// open it with the keys the entrypoint gives them.
		data.View = append(data.View, field.Name)
	}

	writesBody := op.Kind == ir.TableOperationCreate || op.Kind == ir.TableOperationUpdate
	switch {
	case writesBody:
		body := schema.Inputs[endpoint.InputType]
		if body == nil {
			body = schema.Types[endpoint.InputType]
		}
		if info.Input == nil || op.Input == "" || body == nil || body.Source == nil {
			return refuse("whose %s takes no body the router decodes as an input type (input %q)", op.Kind, op.Input)
		}
		for _, field := range body.Fields {
			if field != nil {
				data.Body = append(data.Body, field.Name)
			}
		}
		table.Input = "input"
	case info.Input != nil || len(info.BodyArgs) > 0 || op.Input != "":
		return refuse("whose %s takes a body", op.Kind)
	}

	if op.Kind == ir.TableOperationList {
		if info.Page == nil {
			return refuse("that lists %s and is not @paginated", op.Type)
		}
		for i, param := range endpoint.ArgQueryParams() {
			if !param.Search {
				return refuse("that lists %s by a query parameter, %s, which nothing compares", op.Type, param.Name)
			}
			for _, arg := range info.QueryArgs {
				if arg.Name == param.Name {
					table.Search = arg.Field
					table.SearchOptional = strings.HasPrefix(arg.RustType, "Option<")
				}
			}
			if table.Search == "" || i > 0 {
				return refuse("whose search argument %s the router decodes into no one field of its arguments", param.Name)
			}
			data.Search = param.Name
		}
		if info.Filter != nil {
			table.Filter, table.FilterField = info.Filter.Fields, info.Filter.Field
		}
	} else if info.Page != nil || info.Filter != nil || len(info.QueryArgs) > 0 {
		return refuse("whose %s takes a page, a filter or a query parameter", op.Kind)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return nil, "", err
	}
	table.Literal = rustutil.RawString(strings.TrimSuffix(buf.String(), "\n"))
	return table, "", nil
}

// HasTableOperations reports whether the router serves an operation
// marked @tableOperation (D106): the crate then has src/tables.rs,
// Implementations holds the Tables its handlers serve them from, and the
// crate depends on the database's Rust types for their repositories.
func (o *APIOutput) HasTableOperations() bool {
	return len(o.TableEndpoints()) > 0
}

// TableEndpoints are the table operations the router serves, in route
// order.
func (o *APIOutput) TableEndpoints() []EndpointInfo {
	var out []EndpointInfo
	for _, endpoint := range o.Endpoints {
		if endpoint.Table != nil {
			out = append(out, endpoint)
		}
	}
	return out
}

// TableDatabase is the DB service the table operations are served from,
// the API's database; empty without one.
func (o *APIOutput) TableDatabase() string {
	for _, endpoint := range o.Endpoints {
		if endpoint.Table != nil {
			return endpoint.Table.Database
		}
	}
	return ""
}

// TablesListPages reports whether a table operation lists rows, and so
// answers the runtime's Page.
func (o *APIOutput) TablesListPages() bool {
	for _, endpoint := range o.TableEndpoints() {
		if endpoint.Table.List() {
			return true
		}
	}
	return false
}

// TablesReadInput reports whether a table operation takes a body.
func (o *APIOutput) TablesReadInput() bool {
	for _, endpoint := range o.TableEndpoints() {
		if endpoint.Table.Input != "" {
			return true
		}
	}
	return false
}

// TablesFilter reports whether a table operation's list takes a filter.
func (o *APIOutput) TablesFilter() bool {
	for _, endpoint := range o.TableEndpoints() {
		if len(endpoint.Table.Filter) > 0 {
			return true
		}
	}
	return false
}

// TableArgs are the Args structs of the table operations, sorted, as
// src/tables.rs imports them.
func (o *APIOutput) TableArgs() []string {
	var names []string
	for _, endpoint := range o.TableEndpoints() {
		if endpoint.HasArgs() {
			names = append(names, endpoint.ArgsName)
		}
	}
	slices.Sort(names)
	return names
}

// RouterServed reports whether the router serves the operation itself: a
// behavior operation, which it hands to the behaviors' core (D103), and a
// table operation, which it serves from the table's repository (D106).
// Its namespace's trait has no method for it.
func (e EndpointInfo) RouterServed() bool {
	return e.Behavior != nil || e.Table != nil
}

// checkTableDeps refuses table operations the crate cannot serve: an API
// with no database of its own in Deps, one whose operations are of another
// DB service, and a database whose Rust types are not generated.
func (o *APIOutput) checkTableDeps() error {
	if !o.HasTableOperations() {
		return nil
	}
	db := o.TableDatabase()
	switch {
	case o.Deps == nil || o.Deps.Database == "":
		return fmt.Errorf("rustrestgen: %s has @tableOperation operations over %s (D106), and its Deps hold no database; set its authDb to %s, or depend on it", o.SchemaName, db, db)
	case o.Deps.Database != db:
		return fmt.Errorf("rustrestgen: %s has @tableOperation operations over %s (D106), and its Deps hold %s's pool", o.SchemaName, db, o.Deps.Database)
	case o.Deps.DatabaseTypesCrate == "":
		return fmt.Errorf("rustrestgen: %s has @tableOperation operations over %s (D106), whose repositories are in its Rust types, and %s generates none; enable outputs.types.rust in %s's config", o.SchemaName, db, db, db)
	}
	return nil
}

// TablesInterfacesUse is src/tables.rs's import of the table operations'
// Args structs.
func (o *APIOutput) TablesInterfacesUse() string {
	return rustutil.UseLine("crate::interfaces", o.TableArgs())
}

// checkSQLiteDeps refuses what a Rust server on a SQLite file does not run
// yet (D116), each by name: its Deps.db is the file, where the identity
// store and the engine are built over a Postgres pool. Its behaviors run
// on the file, and its workers claim on it, a @worker's queue and a
// @behaviorWorker's alike.
func (o *APIOutput) checkSQLiteDeps(deps *DepsInfo) error {
	if !deps.SQLite || deps.Database == "" {
		return nil
	}
	refuse := func(what string) error {
		return fmt.Errorf("rustrestgen: %s is a Rust API on %s, whose DB service lists sqlite first, and %s on Postgres alone so far (D116); list postgres first in %s's outputs.sql.dialects, or serve the API in TypeScript", o.SchemaName, deps.Database, what, deps.Database)
	}
	switch {
	case o.Identity != nil:
		return refuse("the Rust server builds its identity store")
	case len(o.Derivers) > 0 || deps.Engine != nil:
		return refuse("the Rust server opens an engine")
	}
	return nil
}
