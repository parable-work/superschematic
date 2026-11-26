package typegen

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
)

// captureFixture loads fixture-capture-db and renders its views as its
// create.sql does.
func captureFixture(t *testing.T, sourceSchema string) (Options, func(naming.GoProjectionCapture) *ModuleOutput) {
	t.Helper()
	schema, _ := loadFixture(t, "fixture-capture-db")
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: "fixture-capture-db", SourceSchema: sourceSchema})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{SchemaName: "fixture-capture-db", ModulePath: "example.com/types/fixture-capture-db", Projections: ddl.Projections}
	generate := func(table naming.GoProjectionCapture) *ModuleOutput {
		t.Helper()
		names := naming.Default()
		names.Go.ProjectionCapture = table
		withTable := opts
		withTable.Naming = names
		output, err := Generate(schema, withTable)
		if err != nil {
			t.Fatalf("generate with %+v: %v", table, err)
		}
		return output
	}
	return opts, generate
}

// TestProjectionCaptureSelectsTheViewsOfTheTable: a view is listed when
// it reads exactly the table's setting, no optional setting, and is in the
// table's pool when the table names one. scope.notes reads app.account_id,
// scope.team_notes reads it and the optional app.team_id, and team.notes
// reads app.team_id. A table that selects no view, or no table, writes no
// projection_capture.go.
func TestProjectionCaptureSelectsTheViewsOfTheTable(t *testing.T) {
	_, generate := captureFixture(t, "")
	table := naming.GoProjectionCapture{Queries: "CaptureQueries", UUIDColumns: "CaptureUUIDColumns"}
	for _, tc := range []struct {
		setting, pool string
		want          []string
		uuids         []string
	}{
		{setting: "app.account_id", want: []string{`"scope.notes"`}, uuids: []string{`[]string{"id"}`}},
		{setting: "app.account_id", pool: "scope", want: []string{`"scope.notes"`}, uuids: []string{`[]string{"id"}`}},
		{setting: "app.team_id", want: []string{`"team.notes"`}, uuids: []string{`[]string{"id", "account_id"}`}},
		{setting: "app.account_id", pool: "team"},
		{setting: "app.channel_id"},
	} {
		table.Setting, table.Pool = tc.setting, tc.pool
		output := generate(table)
		var relations, uuids []string
		if output.ProjectionCapture != nil {
			for _, view := range output.ProjectionCapture.Views {
				relations = append(relations, view.RelationLiteral)
				uuids = append(uuids, view.UUIDColumnsLiteral)
			}
		}
		if !slices.Equal(relations, tc.want) || !slices.Equal(uuids, tc.uuids) {
			t.Errorf("setting %s, pool %q: lists %v with uuid columns %v, want %v with %v", tc.setting, tc.pool, relations, uuids, tc.want, tc.uuids)
		}
		dir := t.TempDir()
		if err := WriteTypes(output, dir); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(dir, "projection_capture.go"))
		if wrote := err == nil; wrote != (len(tc.want) > 0) {
			t.Errorf("setting %s, pool %q: projection_capture.go written = %v, with %d views", tc.setting, tc.pool, wrote, len(tc.want))
		}
	}
	if output := generate(naming.GoProjectionCapture{}); output.ProjectionCapture != nil {
		t.Errorf("no table lists %+v", output.ProjectionCapture)
	}
}

// TestProjectionCaptureQueryIsTheViewsSelect: the capture query is the
// SELECT the view's DDL wraps, over the tables of the build's
// sourceSchema.
func TestProjectionCaptureQueryIsTheViewsSelect(t *testing.T) {
	opts, generate := captureFixture(t, "notes")
	output := generate(naming.GoProjectionCapture{Queries: "CaptureQueries", UUIDColumns: "CaptureUUIDColumns", Setting: "app.account_id"})
	var view sqlgen.ProjectionView
	for _, candidate := range opts.Projections {
		if candidate.Relation == "scope.notes" {
			view = candidate
		}
	}
	ddl := sqlgen.ProjectionViewStatements(view)[0]
	query := sqlgen.ProjectionSelectSQL(view)
	if !strings.HasSuffix(ddl, "\n"+query) || !strings.Contains(query, "\nFROM notes.note AS base\n") {
		t.Fatalf("the view's DDL\n%s\ndoes not wrap the query over notes.note\n%s", ddl, query)
	}
	if got := output.ProjectionCapture.Views[0].SQLLiteral; got != strconv.Quote(query) {
		t.Errorf("SQLLiteral = %s, want %s", got, strconv.Quote(query))
	}
}

// TestProjectionCaptureRefusesAFunctionNameTheModuleDeclares: a capture
// function named like a type of the module fails the generation.
func TestProjectionCaptureRefusesAFunctionNameTheModuleDeclares(t *testing.T) {
	opts, _ := captureFixture(t, "")
	schema, _ := loadFixture(t, "fixture-capture-db")
	opts.Naming = naming.Default()
	opts.Naming.Go.ProjectionCapture = naming.GoProjectionCapture{Queries: "CaptureQueries", UUIDColumns: "Note", Setting: "app.account_id"}
	_, err := Generate(schema, opts)
	if err == nil || !strings.Contains(err.Error(), "go.projection_capture names the function Note, which type Note already declares") {
		t.Fatalf("err = %v, want the collision with type Note", err)
	}
}
