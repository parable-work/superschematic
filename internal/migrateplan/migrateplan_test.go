package migrateplan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// The documents of shop-db, a DB service in the data form, as format
// --to=json writes one per schema file: Order in one file, Customer in
// another.
const (
	orderDocument = `{
		"name": "shop-db", "kind": "DB",
		"types": {"Order": {"name": "Order", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "total", "typeRef": {"name": "number"}, "required": true}
		]}}
	}`
	customerDocument = `{
		"name": "shop-db", "kind": "DB",
		"types": {"Customer": {"name": "Customer", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "email", "typeRef": {"name": "string"}, "required": true, "unique": true}
		]}}
	}`
)

// orderWith is orderDocument with Order's total replaced by the fields
// given, each a field's JSON.
func orderWith(fields ...string) string {
	return `{
		"name": "shop-db", "kind": "DB",
		"types": {"Order": {"name": "Order", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}` + prefixed(fields) + `
		]}}
	}`
}

func prefixed(fields []string) string {
	var b strings.Builder
	for _, field := range fields {
		b.WriteString(", ")
		b.WriteString(field)
	}
	return b.String()
}

func sources(docs ...string) []Source {
	out := make([]Source, len(docs))
	for i, doc := range docs {
		out[i] = Source{Name: []string{"order.schema.json", "customer.schema.json", "third.schema.json"}[i], Data: []byte(doc)}
	}
	return out
}

func tableNames(model *sqlmigrate.Model) []string {
	var names []string
	for _, table := range model.Tables {
		names = append(names, table.Name)
	}
	return names
}

// TestDocumentsMergeOneDatabasesFiles: the documents of one DB service's
// schema files merge into its schema, as a load of its directory merges
// them, and resolve to the model of both files' tables.
func TestDocumentsMergeOneDatabasesFiles(t *testing.T) {
	for _, dialect := range []sqlmigrate.Dialect{sqlmigrate.Postgres, sqlmigrate.SQLite} {
		schema, model, err := Documents(sources(orderDocument, customerDocument), dialect, Env{})
		require.NoError(t, err, dialect)
		assert.Equal(t, "shop-db", schema.Name)
		assert.Contains(t, schema.Types, "Order")
		assert.Contains(t, schema.Types, "Customer")
		assert.Equal(t, "shop-db", model.Service)
		assert.Equal(t, dialect, model.Dialect)
		assert.ElementsMatch(t, []string{"customer", "order"}, tableNames(model))
	}
}

// TestLoadRefusesWhatIsNotOneDatabasesDocuments: each refusal names the
// document and what is wrong with it.
func TestLoadRefusesWhatIsNotOneDatabasesDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		docs []string
		want string
	}{
		{"no documents", nil, "no schema documents: a plan from documents takes every schema document of one DB service"},
		{"not JSON", []string{`{"name": "shop-db",`}, "not a schema document: order.schema.json: unexpected end of JSON input"},
		{"not the data form", []string{`{"name": "shop-db", "kind": "DB", "tables": {}}`}, "not a schema document: order.schema.json"},
		{"a field the IR does not have", []string{orderWith(`{"name": "note", "typeRef": {"name": "string"}, "colour": "red"}`)}, "not a schema document: order.schema.json"},
		{"one definition, with no service", []string{`{"name": "Order", "role": "DBTable", "fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}]}`}, "order.schema.json: the document names no service (name) or no kind (kind)"},
		{"no kind", []string{`{"name": "shop-db", "types": {}}`}, "order.schema.json: the document names no service (name) or no kind (kind)"},
		{"two services", []string{orderDocument, strings.Replace(customerDocument, `"shop-db"`, `"crm-db"`, 1)}, "order.schema.json is a document of shop-db, and customer.schema.json of crm-db: a plan from documents takes one DB service's documents"},
		{"two kinds", []string{orderDocument, `{"name": "shop-db", "kind": "General", "types": {}}`}, "order.schema.json names the kind DB, and customer.schema.json the kind General"},
		{"no database", []string{`{"name": "shop-api", "kind": "API", "types": {}}`}, "shop-api is of kind API, which has no database; plan a DB service's documents"},
		{"another service's type", []string{`{"name": "shop-db", "kind": "DB", "imports": [{"package": "@schemas/shop-common", "types": ["Money"]}], "types": {}}`}, "order.schema.json: the document imports Money from @schemas/shop-common; a plan from documents takes one DB service's documents and resolves no other service"},
		{"a type the documents do not declare", []string{orderWith(`{"name": "customer", "typeRef": {"name": "Customer"}, "required": true, "relation": {"type": "Customer"}}`)}, "Customer"},
	} {
		_, err := Load(sources(tc.docs...), sqlmigrate.Postgres, Env{})
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
}

// TestDocumentsRefuseQueriesOnPostgres: a Postgres plan prepares a
// service's queries on its new model (D86), which a plan from documents
// does not, so it refuses documents that declare one. SQLite prepares
// none.
func TestDocumentsRefuseQueriesOnPostgres(t *testing.T) {
	doc := `{
		"name": "ledger-db", "kind": "DB",
		"types": {
			"Line": {"name": "Line", "role": "DBTable", "fields": [
				{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
				{"name": "amount", "typeRef": {"name": "number"}, "required": true}
			]},
			"TotalsParams": {"name": "TotalsParams", "role": "EmbeddedStruct", "fields": [
				{"name": "least", "typeRef": {"name": "number"}, "required": true}
			]},
			"TotalsRow": {"name": "TotalsRow", "role": "EmbeddedStruct", "fields": [
				{"name": "total", "typeRef": {"name": "number"}, "required": true}
			]},
			"Totals": {"name": "Totals", "role": "Query", "query": {
				"name": "totals", "params": "TotalsParams", "row": "TotalsRow",
				"sql": "SELECT SUM(amount) AS total FROM line WHERE amount >= :least"
			}}
		}
	}`
	_, _, err := Documents(sources(doc), sqlmigrate.Postgres, Env{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ledger-db declares queries (@query), which a Postgres plan prepares on the new model first (D86) and a plan from documents does not: plan the service directory")
}

// TestPlanFromTheModelItselfIsEmpty: a plan from the model the documents
// resolve to has no step, and starts and ends at the same hash.
func TestPlanFromTheModelItselfIsEmpty(t *testing.T) {
	for _, dialect := range []sqlmigrate.Dialect{sqlmigrate.Postgres, sqlmigrate.SQLite} {
		_, model, err := Documents(sources(orderDocument, customerDocument), dialect, Env{})
		require.NoError(t, err)
		plan, err := Plan(sources(customerDocument, orderDocument), model, dialect, nil, Env{})
		require.NoError(t, err, dialect)
		assert.Empty(t, plan.Steps, dialect)
		assert.Equal(t, plan.From, plan.To, dialect)
		assert.Empty(t, plan.Hazards(), dialect)
	}
}

// TestPlanChecksTheModel: the model a plan starts from is the database's,
// of the documents' service in the dialect planned.
func TestPlanChecksTheModel(t *testing.T) {
	_, model, err := Documents(sources(orderDocument), sqlmigrate.Postgres, Env{})
	require.NoError(t, err)

	other := *model
	other.Service = "crm-db"
	_, err = Plan(sources(orderDocument), &other, sqlmigrate.Postgres, nil, Env{})
	require.Error(t, err)
	assert.Equal(t, "from: the model is crm-db's, not shop-db's", err.Error())

	_, err = Plan(sources(orderDocument), model, sqlmigrate.SQLite, nil, Env{})
	require.Error(t, err)
	assert.Equal(t, "from: the model is for postgres, not sqlite", err.Error())

	future := *model
	future.Version = sqlmigrate.ModelVersion + 1
	_, err = Plan(sources(orderDocument), &future, sqlmigrate.Postgres, nil, Env{})
	require.Error(t, err)
	assert.Equal(t, "from: model version 2; this compiler reads version 1", err.Error())
}

// TestReadSources: a stream holds the documents one JSON value after
// another, as format --to=json --stdout prints them file after file.
func TestReadSources(t *testing.T) {
	got, err := ReadSources(strings.NewReader(orderDocument+"\n"+customerDocument+"\n"), "stdin")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "stdin #1", got[0].Name)
	assert.Equal(t, "stdin #2", got[1].Name)
	_, _, err = Documents(got, sqlmigrate.Postgres, Env{})
	require.NoError(t, err)

	_, err = ReadSources(strings.NewReader(" \n"), "stdin")
	require.Error(t, err)
	assert.Equal(t, "stdin holds no schema document", err.Error())

	_, err = ReadSources(strings.NewReader(orderDocument+"\n{"), "stdin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin #2: not JSON: unexpected EOF")
}

// request is a plan request for shop-db from the documents given, with the
// model members as from.
func request(from string, docs ...string) string {
	raw := make([]string, len(docs))
	copy(raw, docs)
	fromMember := ""
	if from != "" {
		fromMember = `, "from": ` + from
	}
	return `{"version": 1, "dialect": "postgres", "documents": [` + strings.Join(raw, ", ") + `]` + fromMember + `}`
}

func answer(t *testing.T, body string) (*Response, error) {
	t.Helper()
	req, err := ReadRequest(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	return Answer(req, Env{})
}

// TestAnswer: a request names the model it plans from, or null for an
// empty database; the response holds the plan as --out writes it, its
// hazards, and the ids of those of a failOn class that allow does not
// name.
func TestAnswer(t *testing.T) {
	first, err := answer(t, request("null", orderDocument))
	require.NoError(t, err)
	var plan sqlmigrate.Plan
	require.NoError(t, json.Unmarshal(first.Plan, &plan))
	assert.Empty(t, plan.From, "null is an empty database")
	assert.NotEmpty(t, plan.Steps)
	canonical, err := plan.CanonicalJSON()
	require.NoError(t, err)
	assert.Equal(t, string(canonical), string(first.Plan), "the plan is its canonical JSON")
	assert.Empty(t, first.Refused)

	// The next version drops total and adds amount: a destructive drop,
	// refused until allowed, or no drop at all when it is a rename.
	next := orderWith(`{"name": "amount", "typeRef": {"name": "number"}, "required": true}`)
	from := string(plan.ToModel)
	const drop = "destructive:table/order/column/total"
	for _, tc := range []struct {
		extra   string
		refused []string
		hazard  bool
	}{
		{``, nil, true},
		{`, "failOn": ["destructive"]`, []string{drop}, true},
		{`, "failOn": ["destructive"], "allow": ["` + drop + `"]`, nil, true},
		// amount is required with no default: the previous servers do not
		// write it, and adding it fails on a table with rows.
		{`, "failOn": ["all"], "allow": ["` + drop + `"]`, []string{"compat:table/order/column/amount", "data-dependent:table/order/column/amount"}, true},
		{`, "failOn": ["destructive"], "renames": ["order.total=order.amount"]`, nil, false},
	} {
		body := strings.TrimSuffix(request(from, next), "}") + tc.extra + "}"
		response, err := answer(t, body)
		require.NoError(t, err, tc.extra)
		assert.Equal(t, tc.refused, response.Refused, tc.extra)
		var ids []string
		for _, hazard := range response.Hazards {
			ids = append(ids, hazard.ID)
		}
		if tc.hazard {
			assert.Contains(t, ids, drop, tc.extra)
		} else {
			assert.NotContains(t, ids, drop, tc.extra)
		}
		var next sqlmigrate.Plan
		require.NoError(t, json.Unmarshal(response.Plan, &next))
		assert.Equal(t, plan.To, next.From, "the plan starts from the model given")
	}

	// The model a plan ends at, planned again, is an empty plan.
	again, err := answer(t, request(from, orderDocument))
	require.NoError(t, err)
	var empty sqlmigrate.Plan
	require.NoError(t, json.Unmarshal(again.Plan, &empty))
	assert.Empty(t, empty.Steps)
	assert.Equal(t, empty.From, empty.To)
	assert.Empty(t, again.Hazards)
}

// TestAnswerRefuses: what a request gets wrong is refused by name, before
// any plan is made.
func TestAnswerRefuses(t *testing.T) {
	_, model, err := Documents(sources(orderDocument), sqlmigrate.Postgres, Env{})
	require.NoError(t, err)
	modelJSON, err := model.CanonicalJSON()
	require.NoError(t, err)
	from := string(modelJSON)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"empty", ``, "the request is empty: want a JSON object with version, dialect, documents and from"},
		{"not JSON", `{"version": 1,`, "not a plan request (unexpected EOF)"},
		{"two values", request("null", orderDocument) + ` {}`, "not a plan request: more than one JSON value"},
		{"an unknown member", `{"version": 1, "dialect": "postgres", "documents": [], "from": null, "rename": []}`, `not a plan request (json: unknown field "rename")`},
		{"another version", `{"version": 2, "dialect": "postgres", "documents": [], "from": null}`, "request version 2; this compiler reads version 1"},
		{"no dialect", `{"version": 1, "documents": [], "from": null}`, "the request names no dialect (dialect): want postgres or sqlite"},
		{"another dialect", `{"version": 1, "dialect": "mysql", "documents": [], "from": null}`, `dialect "mysql": want postgres or sqlite`},
		{"no model", request("", orderDocument), "the request names no model to plan from (from): give the model the database recorded, as superschematic-migrate status --model prints it, or null for an empty database"},
		{"not a model", request(`{"version": 1, "tables": 3}`, orderDocument), "from: not a model (json: cannot unmarshal number"},
		{"a member a model does not have", request(`{"version": 1, "dialect": "postgres", "service": "shop-db", "sequences": []}`, orderDocument), `from: not a model (json: unknown field "sequences")`},
		{"another service's model", request(strings.Replace(from, `"shop-db"`, `"crm-db"`, 1), orderDocument), "from: the model is crm-db's, not shop-db's"},
		{"another dialect's model", strings.Replace(request(from, orderDocument), `"dialect": "postgres", "documents"`, `"dialect": "sqlite", "documents"`, 1), "from: the model is for postgres, not sqlite"},
		{"no documents", request("null"), "no schema documents: a plan from documents takes every schema document of one DB service"},
		{"a malformed document", request("null", `{"name": "shop-db", "kind": "DB", "types": {"Order": 3}}`), "not a schema document: documents[0]"},
		{"a bad rename", strings.TrimSuffix(request("null", orderDocument), "}") + `, "renames": ["order.total=amount"]}`, `renames: rename "order.total=amount": want old=new for a table`},
		{"a bad class", strings.TrimSuffix(request("null", orderDocument), "}") + `, "failOn": ["oops"]}`, `failOn: "oops" is not a hazard class: want destructive, blocking, compat, data-dependent, copy-table, api-breaking, history, raw-sql, or all`},
		{"a bad id", strings.TrimSuffix(request("null", orderDocument), "}") + `, "allow": ["table/order"]}`, `allow: "table/order" is not a hazard id: want <class>:<subject>, as the plan lists it`},
		{"a rename of nothing", strings.TrimSuffix(request("null", orderDocument), "}") + `, "renames": ["order.total=order.amount"]}`, "--rename order.total=order.amount"},
	} {
		_, err := answer(t, tc.body)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
}

// TestResponseJSON: a response is one line of JSON, its plan's bytes as
// they are, with no HTML escaping, and empty lists rather than none; an
// error's response holds the error alone.
func TestResponseJSON(t *testing.T) {
	plan := json.RawMessage(`{"steps":[{"statements":["CHECK (\"total\" >= 0 AND \"note\" <> '')"]}]}`)
	line, err := (&Response{Plan: plan}).JSON()
	require.NoError(t, err)
	assert.Equal(t, `{"version":1,"plan":`+string(plan)+`,"hazards":[],"refused":[]}`+"\n", string(line))
	assert.Equal(t, 1, bytes.Count(line, []byte("\n")))

	line, err = ErrorResponse(assert.AnError).JSON()
	require.NoError(t, err)
	assert.Equal(t, `{"version":1,"error":"`+assert.AnError.Error()+`"}`+"\n", string(line))
}
