package gosdkgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	ir "github.com/parable-work/superschematic/ir"
)

// loadUnionListsAPI loads union-lists-api (sdktest.LoadUnionListsService):
// the union Shape as a list, a list of lists, one value, a map, a map of
// lists, and one value or a list that may be null, none paged.
func loadUnionListsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  sdktest.UnionListsService,
		ModulePath:  "example.com/schemas/api/" + sdktest.UnionListsService,
		TypesModule: "example.com/schemas/types/go/" + sdktest.UnionListsService,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// writeUnionListsSDK writes the Go types module and the Go SDK of
// union-lists-api into a temp tree, and returns the SDK's directory and the
// output it was written from. The SDK reads which types are unions from
// the types module's unions.go.
func writeUnionListsSDK(t *testing.T, schema *ir.Schema, apiOutput *apigen.APIOutput) (string, *SDKOutput) {
	t.Helper()
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", sdktest.UnionListsService)
	typesDir := filepath.Join(root, "types", "go", sdktest.UnionListsService)
	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: sdktest.UnionListsService,
		ModulePath: "example.com/schemas/types/go/" + sdktest.UnionListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+sdktest.UnionListsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkDir, sdkOutput
}

// TestUnionListsDecodeThroughTheirWrappers: a response of a union that does
// not page, a list, a list of lists, one value, a map or a map of lists,
// decodes through types.ShapeWrapper at the response's list levels and
// keys, and its method returns the union at the same levels and keys. One
// value that may be null decodes through a pointer to the wrapper.
func TestUnionListsDecodeThroughTheirWrappers(t *testing.T) {
	schema, apiOutput := loadUnionListsAPI(t)
	_, sdkOutput := writeUnionListsSDK(t, schema, apiOutput)
	want := map[string]struct{ output, wrapper string }{
		"ListShapes":   {"[]types.Shape", "[]types.ShapeWrapper"},
		"ShapeRows":    {"[][]types.Shape", "[][]types.ShapeWrapper"},
		"GetShape":     {"types.Shape", "types.ShapeWrapper"},
		"ShapesByName": {"map[string]types.Shape", "map[string]types.ShapeWrapper"},
		"ShapeGroups":  {"map[string][]types.Shape", "map[string][]types.ShapeWrapper"},
		"FindShape":    {"types.Shape", "*types.ShapeWrapper"},
		"FindShapes":   {"[]types.Shape", "[]types.ShapeWrapper"},
	}
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			w, ok := want[ep.MethodName]
			if !ok {
				t.Errorf("unexpected endpoint %s", ep.MethodName)
				continue
			}
			delete(want, ep.MethodName)
			if !ep.IsUnionOutput || ep.OutputGoType != w.output || ep.OutputUnionWrapperGoType != w.wrapper || ep.OutputUnionGoType != "types.Shape" {
				t.Errorf("%s = (union %v, %s, %s, %s), want (%s, %s, types.Shape)", ep.MethodName, ep.IsUnionOutput, ep.OutputGoType, ep.OutputUnionWrapperGoType, ep.OutputUnionGoType, w.output, w.wrapper)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing endpoints %v", want)
	}
}

// TestWriteSDKGoldenUnionLists pins the namespace file of the Go SDK for
// union-lists-api. Regenerate with
// go test ./internal/generator/gosdkgen -run TestWriteSDKGoldenUnionLists -update
func TestWriteSDKGoldenUnionLists(t *testing.T) {
	schema, apiOutput := loadUnionListsAPI(t)
	sdkDir, _ := writeUnionListsSDK(t, schema, apiOutput)
	const name = "namespaces/shape.go"
	got, err := os.ReadFile(filepath.Join(sdkDir, name))
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("testdata", "golden", sdktest.UnionListsService, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s (run with -update): %v", name, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from golden (run with -update to accept)", name)
	}
}

// TestUnionListsSDKBuildsAndRuns runs unionListsSDKTest in the generated
// SDK against an httptest server: a list, a list of lists and one value of
// the union decode into its members, and an element the union cannot read
// is an error.
func TestUnionListsSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadUnionListsAPI(t)
	runInSDK(t, schema, apiOutput, sdktest.UnionListsService, "union_lists_test.go", unionListsSDKTest)
}

// unionListsSDKTest runs in the generated SDK module against a server that
// answers each operation with circles and squares mixed, or, when bad,
// each list with a triangle among them, an element the union cannot read.
const unionListsSDKTest = `package sdk_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "example.com/schemas/sdk/go/union-lists-api"
	types "example.com/schemas/types/go/union-lists-api"
)

const circle = ` + "`" + `{"radius":1}` + "`" + `

const square = ` + "`" + `{"side":2}` + "`" + `

// triangle is a string, which neither Circle nor Square reads.
const triangle = ` + "`" + `"triangle"` + "`" + `

func serveShapes(t *testing.T, bad bool) *sdk.UnionListsApiSDK {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch {
		case strings.HasSuffix(r.URL.Path, "/shapes/rows") && bad:
			data = "[[" + circle + "],[" + square + "," + triangle + "]]"
		case strings.HasSuffix(r.URL.Path, "/shapes/rows"):
			data = "[[" + circle + "],[" + square + "," + circle + "]]"
		case strings.HasSuffix(r.URL.Path, "/shapes") && bad:
			data = "[" + circle + "," + triangle + "]"
		case strings.HasSuffix(r.URL.Path, "/shapes"):
			data = "[" + circle + "," + square + "]"
		default:
			data = square
		}
		_, _ = w.Write([]byte(` + "`" + `{"data":` + "`" + ` + data + ` + "`" + `,"meta":{"requestId":"req-1"}}` + "`" + `))
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func describe(shape types.Shape) string {
	switch member := shape.(type) {
	case types.Circle:
		return fmt.Sprintf("circle %v", member.Radius)
	case types.Square:
		return fmt.Sprintf("square %v", member.Side)
	}
	return "unknown"
}

func describeAll(shapes []types.Shape) string {
	described := make([]string, len(shapes))
	for i, shape := range shapes {
		described[i] = describe(shape)
	}
	return strings.Join(described, ", ")
}

func TestListShapesDecodesEachElementThroughTheUnion(t *testing.T) {
	shapes, err := serveShapes(t, false).ShapeNamespace.ListShapes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := describeAll(shapes); got != "circle 1, square 2" {
		t.Fatalf("shapes = %s", got)
	}
}

func TestShapeRowsDecodesEachElementOfEachRow(t *testing.T) {
	rows, err := serveShapes(t, false).ShapeNamespace.ShapeRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || describeAll(rows[0]) != "circle 1" || describeAll(rows[1]) != "square 2, circle 1" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestGetShapeDecodesOneValue(t *testing.T) {
	shape, err := serveShapes(t, false).ShapeNamespace.GetShape(context.Background(), "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := describe(shape); got != "square 2" {
		t.Fatalf("shape = %s", got)
	}
}

func TestAnElementTheUnionCannotReadIsAnError(t *testing.T) {
	client := serveShapes(t, true)
	if _, err := client.ShapeNamespace.ListShapes(context.Background()); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("ListShapes = %v", err)
	}
	if _, err := client.ShapeNamespace.ShapeRows(context.Background()); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("ShapeRows = %v", err)
	}
}
`
