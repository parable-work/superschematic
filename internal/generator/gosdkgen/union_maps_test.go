package gosdkgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestUnionMapsAndNullsSDKBuildsAndRuns runs unionMapsSDKTest in the
// generated SDK of union-lists-api against an httptest server: a map and a
// map of lists of the union decode into its members by key, an element the
// union cannot read or a null element is an error, and a response that may
// be null, one value or a list, returns nil for null.
func TestUnionMapsAndNullsSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadUnionListsAPI(t)
	runInSDK(t, schema, apiOutput, sdktest.UnionListsService, "union_maps_test.go", unionMapsSDKTest)
}

// unionMapsSDKTest runs in the generated SDK module. Each test serves the
// data each path answers with, by the path's last segments.
const unionMapsSDKTest = `package sdk_test

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

// serve answers a request whose path ends in "/" and a key of answers with
// the key's data in the response envelope.
func serve(t *testing.T, answers map[string]string) *sdk.UnionListsApiSDK {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for suffix, data := range answers {
			if strings.HasSuffix(r.URL.Path, "/"+suffix) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(` + "`" + `{"data":` + "`" + ` + data + ` + "`" + `,"meta":{"requestId":"req-1"}}` + "`" + `))
				return
			}
		}
		t.Errorf("unexpected request %s", r.URL.Path)
		http.NotFound(w, r)
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
	case nil:
		return "nil"
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

func TestShapesByNameDecodesEachValueThroughTheUnion(t *testing.T) {
	client := serve(t, map[string]string{"shape-index": "{\"a\":" + circle + ",\"b\":" + square + "}"})
	shapes, err := client.ShapeNamespace.ShapesByName(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(shapes) != 2 || describe(shapes["a"]) != "circle 1" || describe(shapes["b"]) != "square 2" {
		t.Fatalf("shapes = %v", shapes)
	}
}

func TestShapeGroupsDecodesEachElementOfEachList(t *testing.T) {
	client := serve(t, map[string]string{"shape-groups": "{\"round\":[" + circle + "],\"mixed\":[" + square + "," + circle + "],\"none\":[]}"})
	groups, err := client.ShapeNamespace.ShapeGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 || describeAll(groups["round"]) != "circle 1" || describeAll(groups["mixed"]) != "square 2, circle 1" {
		t.Fatalf("groups = %v", groups)
	}
	if none, ok := groups["none"]; !ok || none == nil || len(none) != 0 {
		t.Fatalf("groups[none] = %#v, want an empty list", none)
	}
}

func TestAMapElementTheUnionCannotReadIsAnError(t *testing.T) {
	for name, data := range map[string]string{
		"triangle": triangle,
		"null":     "null",
	} {
		client := serve(t, map[string]string{
			"shape-index":  "{\"a\":" + circle + ",\"bad\":" + data + "}",
			"shape-groups": "{\"mixed\":[" + square + "," + data + "]}",
		})
		if shapes, err := client.ShapeNamespace.ShapesByName(context.Background()); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Errorf("ShapesByName with a %s = %v, %v", name, shapes, err)
		}
		if groups, err := client.ShapeNamespace.ShapeGroups(context.Background()); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Errorf("ShapeGroups with a %s = %v, %v", name, groups, err)
		}
	}
}

func TestFindShapeDecodesAShapeOrNull(t *testing.T) {
	shape, err := serve(t, map[string]string{"shape-search/c-1": circle}).ShapeNamespace.FindShape(context.Background(), "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := describe(shape); got != "circle 1" {
		t.Fatalf("shape = %s", got)
	}
	shape, err = serve(t, map[string]string{"shape-search/none": "null"}).ShapeNamespace.FindShape(context.Background(), "none")
	if err != nil || shape != nil {
		t.Fatalf("FindShape answered null = %v, %v; want nil, nil", shape, err)
	}
}

func TestFindShapesDecodesAListOrNull(t *testing.T) {
	shapes, err := serve(t, map[string]string{"shape-search": "[" + square + "," + circle + "]"}).ShapeNamespace.FindShapes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := describeAll(shapes); got != "square 2, circle 1" {
		t.Fatalf("shapes = %s", got)
	}
	shapes, err = serve(t, map[string]string{"shape-search": "null"}).ShapeNamespace.FindShapes(context.Background())
	if err != nil || shapes != nil {
		t.Fatalf("FindShapes answered null = %#v, %v; want nil, nil", shapes, err)
	}
	shapes, err = serve(t, map[string]string{"shape-search": "[]"}).ShapeNamespace.FindShapes(context.Background())
	if err != nil || shapes == nil || len(shapes) != 0 {
		t.Fatalf("FindShapes answered [] = %#v, %v; want an empty list", shapes, err)
	}
}

func TestARequiredShapeThatAnswersNullIsAnError(t *testing.T) {
	shape, err := serve(t, map[string]string{"shapes/s-1": "null"}).ShapeNamespace.GetShape(context.Background(), "s-1")
	if err == nil {
		t.Fatalf("GetShape answered null = %v, want an error", shape)
	}
}
`
