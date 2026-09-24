package apigen_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestNestedArraysGuard: apigen refuses arrays of arrays (T[][]) until it
// renders them, rather than emitting T[]. The change that teaches apigen
// T[][] deletes its nested-arrays guard and replaces this test with output
// for the fixture-nested-arrays services.
// Both entries are guarded: Generate (routes and the OpenAPI document) and
// TypeOpenAPISchema (the standalone schema the Go types generator embeds).
func TestNestedArraysGuard(t *testing.T) {
	const want = "apigen does not support arrays of arrays yet ("
	api, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays-api"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apigen.Generate(api, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "fixture-nested-arrays-api"}); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("Generate: err = %v", err)
	}

	general, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apigen.TypeOpenAPISchema("Drawing", general, nil); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("TypeOpenAPISchema: err = %v", err)
	}
}

// TestAPIOutputFindArrayOfArrays: the SDK generators' guards read nested
// arrays off the API output, where apigen carries IsArrayOfArrays on every
// Param and on the endpoint response.
func TestAPIOutputFindArrayOfArrays(t *testing.T) {
	var none *apigen.APIOutput
	if where, found := none.FindArrayOfArrays(); found {
		t.Fatalf("nil output: %q", where)
	}
	nested := apigen.Param{Name: "cells", IsArray: true, IsArrayOfArrays: true}
	cases := []struct {
		out  apigen.APIOutput
		want string
	}{
		{apigen.APIOutput{TypeFields: map[string][]apigen.Param{"Grid": {{Name: "id"}, nested}}}, "Grid.cells"},
		{apigen.APIOutput{Endpoints: []apigen.EndpointInfo{{Namespace: "grid", Name: "rows", OutputIsArray: true, OutputIsArrayOfArrays: true}}}, "grid.rows"},
		{apigen.APIOutput{Endpoints: []apigen.EndpointInfo{{Namespace: "grid", Name: "save", ScalarArgs: []apigen.Param{nested}}}}, "grid.save(cells)"},
		{apigen.APIOutput{Endpoints: []apigen.EndpointInfo{{Namespace: "grid", Name: "save", InputTypeFields: []apigen.Param{nested}}}}, "grid.save(cells)"},
		{apigen.APIOutput{Endpoints: []apigen.EndpointInfo{{Namespace: "grid", Name: "list", QueryParams: []apigen.Param{{Name: "ids", IsArray: true}}}}}, ""},
	}
	for _, tc := range cases {
		where, found := tc.out.FindArrayOfArrays()
		if where != tc.want || found != (tc.want != "") {
			t.Errorf("FindArrayOfArrays() = %q, %v; want %q", where, found, tc.want)
		}
	}
}
