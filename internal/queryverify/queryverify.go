package queryverify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The describe document and its result, as the migration runner reads and
// writes them (runtime/migrate/go's DescribeDocument and DescribeResult):
// the compiler holds no Postgres driver, so the two are written apart, and
// TestDescribeDocumentMatchesTheRunner holds them to one JSON form.

// DescribeVersion is the version of the describe document and its result.
const DescribeVersion = 1

// Document is a describe document: the DDL to apply and the statements to
// prepare.
type Document struct {
	Version    int         `json:"version"`
	DDL        string      `json:"ddl"`
	Statements []Statement `json:"statements"`
}

// Statement is one statement of a describe document.
type Statement struct {
	ID  string `json:"id"`
	SQL string `json:"sql"`
}

// Result is what the runner described of each statement, in the
// document's order.
type Result struct {
	Version    int           `json:"version"`
	Statements []Description `json:"statements"`
}

// Description is what Postgres described of one statement, or the error
// it refused it with.
type Description struct {
	ID      string   `json:"id"`
	Error   string   `json:"error,omitempty"`
	Params  []Type   `json:"params,omitempty"`
	Columns []Column `json:"columns,omitempty"`
}

// Type is a type as Postgres names it: its format_type (Type), its pg_type
// name (Name) and, of an array, its element's name.
type Type struct {
	Type    string `json:"type"`
	Name    string `json:"typeName"`
	Element string `json:"element,omitempty"`
}

// Column is a result column, and the table column it comes straight from,
// when it does.
type Column struct {
	Name string `json:"name"`
	Type
	Table   string `json:"table,omitempty"`
	Column  string `json:"column,omitempty"`
	NotNull bool   `json:"notNull,omitempty"`
}

// statementID is the id of a query's i-th statement in a describe document.
func statementID(q sqlutil.QueryModel, i int) string {
	return fmt.Sprintf("%s/%d", q.Name, i)
}

// DocumentOf is the describe document of queries over createSQL.
func DocumentOf(createSQL string, queries []sqlutil.QueryModel) Document {
	doc := Document{Version: DescribeVersion, DDL: createSQL}
	for _, q := range queries {
		for i, st := range q.Statements() {
			doc.Statements = append(doc.Statements, Statement{ID: statementID(q, i), SQL: st.SQL})
		}
	}
	return doc
}

// Describe has the migration runner at runner describe doc on the Postgres
// server url names, in a database it creates there and drops.
func Describe(ctx context.Context, runner, url string, doc Document) (*Result, error) {
	dir, err := os.MkdirTemp("", "superschematic-queries-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "statements.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, runner, "describe", "--statements", path, "--database-url", url)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("the migration runner could not describe the queries: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("the migration runner's description: %w", err)
	}
	if result.Version != DescribeVersion {
		return nil, fmt.Errorf("the migration runner describes version %d; this compiler reads version %d: use the runner of this release", result.Version, DescribeVersion)
	}
	return &result, nil
}

// Judge compares what Postgres described of each query's statements with
// the types the schema declares (D86). It returns the lock of the queries,
// and a problem per way a query does not hold, each naming the query:
//   - Postgres refuses a statement;
//   - a parameter's described type is not of the family of the SQL type
//     its field's column would have (sqlutil.ColumnType), the way a
//     column's type in the DDL maps the field;
//   - the result's columns are not the row's fields' columns, or one's type
//     is not its field's;
//   - a row field that is not Nullable<T> reads a column that may be null:
//     one computed, one of a column that is not NOT NULL (a projection's
//     view's column of a required field is), or one of a table an outer
//     join may leave out, which the query does not vouch for with notNull
//     or a -- @notNull comment.
func Judge(queries []sqlutil.QueryModel, result *Result) (*Lock, []string) {
	described := map[string]Description{}
	for _, d := range result.Statements {
		described[d.ID] = d
	}
	lock := &Lock{Version: LockVersion}
	var problems []string
	for _, q := range queries {
		var entry *LockedQuery
		for i, st := range q.Statements() {
			d, ok := described[statementID(q, i)]
			where := "query " + q.Name
			if st.Label != "" {
				where += " (" + st.Label + ")"
			}
			if !ok {
				problems = append(problems, where+": the runner described no such statement")
				continue
			}
			if d.Error != "" {
				problems = append(problems, fmt.Sprintf("%s: Postgres refuses it: %s", where, d.Error))
				continue
			}
			args := q.Args
			if st.Label == "page after a key" {
				args = append(append([]sqlutil.QueryField(nil), args...), q.Page.Key...)
			}
			problems = append(problems, judgeParams(where, q, args, d.Params)...)
			problems = append(problems, judgeColumns(where, q, d.Columns)...)
			if entry == nil {
				entry = &LockedQuery{Name: q.Name, Hash: Hash(q), Params: []string{}, Columns: []LockedColumn{}}
				for _, p := range d.Params[:min(len(d.Params), len(q.Args))] {
					entry.Params = append(entry.Params, p.Type)
				}
				for _, c := range d.Columns {
					entry.Columns = append(entry.Columns, LockedColumn{Name: c.Name, Type: c.Type.Type, NotNull: straightNotNull(q, c)})
				}
			}
		}
		if entry != nil {
			lock.Queries = append(lock.Queries, *entry)
		}
	}
	return lock, problems
}

func judgeParams(where string, q sqlutil.QueryModel, args []sqlutil.QueryField, params []Type) []string {
	if len(params) != len(args) {
		return []string{fmt.Sprintf("%s: Postgres reads %d parameters and the query binds %d", where, len(params), len(args))}
	}
	var problems []string
	for i, a := range args {
		if !compatible(a.SQLType, params[i]) {
			owner := q.Params
			if i >= len(q.Args) {
				owner = q.Row + "'s page key"
			}
			problems = append(problems, fmt.Sprintf("%s: :%s is %s in %s, which binds as %s, and Postgres reads $%d as %s; cast it in the SQL (:%s::%s) or give the field a type of %s's",
				where, a.Name, a.Def.TypeRef.Name, owner, a.SQLType, i+1, params[i].Type, a.Name, castOf(a.SQLType), params[i].Type))
		}
	}
	return problems
}

func judgeColumns(where string, q sqlutil.QueryModel, columns []Column) []string {
	var problems []string
	byName := map[string]Column{}
	for _, c := range columns {
		if _, twice := byName[c.Name]; twice {
			problems = append(problems, fmt.Sprintf("%s: it returns two columns named %s; give one another name with AS", where, c.Name))
		}
		byName[c.Name] = c
	}
	fields := map[string]bool{}
	for _, f := range q.RowFields {
		fields[f.Column] = true
		c, ok := byName[f.Column]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: it returns no column %s, which %s.%s reads; select it, AS %s", where, f.Column, q.Row, f.Name, f.Column))
			continue
		}
		if !compatible(f.SQLType, c.Type) {
			problems = append(problems, fmt.Sprintf("%s: column %s is %s, and %s.%s is %s, which a %s column holds; cast it in the SQL (::%s) or give the field a type of %s's",
				where, c.Name, c.Type.Type, q.Row, f.Name, f.Def.TypeRef.Name, f.SQLType, castOf(f.SQLType), c.Type.Type))
		}
		if f.Nullable || q.NotNull[f.Name] || q.NotNull[f.Column] || straightNotNull(q, c) {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s: column %s %s, and %s.%s is not Nullable; declare it Nullable<%s>, or vouch that the query never returns null for it with notNull: [%q] or a -- @notNull %s comment",
			where, c.Name, nullReason(q, c), q.Row, f.Name, f.Def.TypeRef.Name, f.Name, f.Name))
	}
	for _, c := range columns {
		if !fields[c.Name] {
			problems = append(problems, fmt.Sprintf("%s: it returns column %s, which no field of %s reads; add a field whose column it is, or leave it out", where, c.Name, q.Row))
		}
	}
	return problems
}

// straightNotNull reports whether column c comes straight from a NOT NULL
// column of a table no outer join of q may leave out: a table's NOT NULL
// column, or a column of a projection's view whose field is required
// (sqlutil.QueryModel.ViewNotNull, D95), which the view's model the lock
// hashes holds.
func straightNotNull(q sqlutil.QueryModel, c Column) bool {
	return c.Table != "" && (c.NotNull || q.ViewNotNull[c.Table+"."+c.Column]) && !outer(q, c.Table)
}

func outer(q sqlutil.QueryModel, table string) bool {
	if q.AllOuter {
		return true
	}
	for _, t := range q.OuterTables {
		if t == table {
			return true
		}
	}
	return false
}

// nullReason says why column c may be null.
func nullReason(q sqlutil.QueryModel, c Column) string {
	switch {
	case c.Table == "":
		return "is computed, which Postgres cannot tell is never null"
	case !c.NotNull && !q.ViewNotNull[c.Table+"."+c.Column]:
		return fmt.Sprintf("reads %s.%s, which may be null", c.Table, c.Column)
	}
	return fmt.Sprintf("reads %s.%s of a table an outer join may leave out", c.Table, c.Column)
}

// families groups Postgres's type names by what a value of each crosses
// as: a type of one family binds and reads as any other of it.
var families = map[string]string{
	"text": "text", "varchar": "text", "bpchar": "text", "name": "text", "citext": "text", "char": "text",
	"int2": "int", "int4": "int", "int8": "int",
	"float4": "float", "float8": "float",
	"numeric": "numeric", "uuid": "uuid", "bool": "bool",
	"timestamptz": "timestamptz", "timestamp": "timestamp", "date": "date", "time": "time",
	"json": "json", "jsonb": "json", "vector": "vector", "interval": "interval", "point": "point", "bytea": "bytea",
}

// declaredNames are the pg_type names of the SQL types the DDL writes.
var declaredNames = map[string]string{
	"TEXT": "text", "VARCHAR": "varchar", "CHARACTER VARYING": "varchar", "CHAR": "bpchar", "CHARACTER": "bpchar", "CITEXT": "citext",
	"SMALLINT": "int2", "INTEGER": "int4", "INT": "int4", "BIGINT": "int8",
	"REAL": "float4", "DOUBLE PRECISION": "float8",
	"NUMERIC": "numeric", "DECIMAL": "numeric", "UUID": "uuid", "BOOLEAN": "bool",
	"TIMESTAMPTZ": "timestamptz", "TIMESTAMP WITH TIME ZONE": "timestamptz", "TIMESTAMP": "timestamp", "DATE": "date", "TIME": "time",
	"JSON": "json", "JSONB": "jsonb", "VECTOR": "vector", "INTERVAL": "interval", "POINT": "point", "BYTEA": "bytea",
}

// baseType is a DDL type without its list suffix and its modifier:
// NUMERIC for NUMERIC(12,2)[].
func baseType(sqlType string) string {
	t := strings.TrimSuffix(strings.TrimSpace(sqlType), "[]")
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	return strings.ToUpper(strings.TrimSpace(t))
}

// castOf is the cast a statement writes to give a value the DDL type
// sqlType, its modifier left out: bigint for BIGINT, uuid[] for UUID[].
func castOf(sqlType string) string {
	cast := strings.ToLower(baseType(sqlType))
	if strings.HasSuffix(strings.TrimSpace(sqlType), "[]") {
		cast += "[]"
	}
	return cast
}

// familyOf is the family of a pg_type name; a type of no family is its own.
func familyOf(name string) string {
	if f, ok := families[name]; ok {
		return f
	}
	return name
}

// compatible reports whether a value of a field whose column would be of
// the DDL type sqlType crosses as a value of the described type: the same
// family, both lists or neither.
func compatible(sqlType string, t Type) bool {
	list := strings.HasSuffix(strings.TrimSpace(sqlType), "[]")
	name := declaredNames[baseType(sqlType)]
	if name == "" {
		name = strings.ToLower(baseType(sqlType))
	}
	described := t.Name
	if t.Element != "" {
		if !list {
			return false
		}
		described = t.Element
	} else if list {
		return false
	}
	return familyOf(name) == familyOf(described)
}

// Options configure Verify.
type Options struct {
	// Schema is the DB service whose queries are verified, and CreateSQL
	// its create.sql on Postgres.
	Schema    *ir.Schema
	CreateSQL string
	// DatabaseURL names a Postgres server whose role may create a
	// database: Verify describes the queries in one of its own there. Empty
	// starts Image in Docker for the run.
	DatabaseURL string
	Image       string
	// Runner is the migration runner, superschematic-migrate.
	Runner string
	// Log reports progress, such as the container it starts; nil is
	// silent.
	Log func(format string, args ...any)
}

// Verify describes the service's queries on Postgres and judges them
// (Judge). It returns the lock and the problems, or an error when it could
// not ask Postgres.
func Verify(ctx context.Context, opts Options) (*Lock, []string, error) {
	queries, err := sqlutil.QueriesOf(opts.Schema, nil)
	if err != nil {
		return nil, nil, err
	}
	if len(queries) == 0 {
		return &Lock{Version: LockVersion, Queries: []LockedQuery{}}, nil, nil
	}
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	url := opts.DatabaseURL
	if url == "" {
		logf("starting %s in Docker to prepare the queries on", opts.Image)
		started, stop, err := StartPostgres(ctx, opts.Image)
		if err != nil {
			return nil, nil, err
		}
		defer stop()
		url = started
	}
	result, err := Describe(ctx, opts.Runner, url, DocumentOf(opts.CreateSQL, queries))
	if err != nil {
		return nil, nil, err
	}
	lock, problems := Judge(queries, result)
	return lock, problems, nil
}

// postgresPassword is the password of the Postgres StartPostgres runs,
// which listens on loopback alone for the run.
const postgresPassword = "superschematic"

// StartPostgres runs image in Docker, publishing Postgres on a loopback
// port, and waits until it takes connections. It returns the server's URL
// and a function that removes the container.
func StartPostgres(ctx context.Context, image string) (string, func(), error) {
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "-d",
		"-e", "POSTGRES_PASSWORD="+postgresPassword, "-p", "127.0.0.1::5432", image).Output()
	if err != nil {
		return "", nil, fmt.Errorf("start Postgres in Docker (%s): %w%s; or pass --database-url naming a Postgres whose role may create a database", image, err, stderrOf(err))
	}
	id := strings.TrimSpace(string(out))
	stop := func() { _ = exec.Command("docker", "rm", "-f", id).Run() }
	port, err := exec.CommandContext(ctx, "docker", "port", id, "5432/tcp").Output()
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("read the Postgres container's port: %w%s", err, stderrOf(err))
	}
	address := strings.TrimSpace(strings.SplitN(string(port), "\n", 2)[0])
	// The image's first server, which initdb runs, listens on its socket
	// alone; ready on TCP is the server that stays.
	deadline := time.Now().Add(90 * time.Second)
	for exec.CommandContext(ctx, "docker", "exec", id, "pg_isready", "-h", "127.0.0.1", "-U", "postgres").Run() != nil {
		if time.Now().After(deadline) || ctx.Err() != nil {
			stop()
			return "", nil, fmt.Errorf("the Postgres container %s did not take connections within 90s", image)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Sprintf("postgres://postgres:%s@%s/postgres?sslmode=disable", postgresPassword, address), stop, nil
}

func stderrOf(err error) string {
	if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
		return ": " + strings.TrimSpace(string(exit.Stderr))
	}
	return ""
}
