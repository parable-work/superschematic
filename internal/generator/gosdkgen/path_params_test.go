package gosdkgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestPathParamsSDKBuildsAndRuns runs pathParamsSDKTest in the Go SDK of
// fixture-nested-arrays-api, with grid.cell added: each path value, one
// with %, /, ?, # or non-ASCII text among them, is sent as one path
// segment, percent-encoded once as url.PathEscape writes it, and an
// httptest server that decodes it once receives the value passed.
func TestPathParamsSDKBuildsAndRuns(t *testing.T) {
	schema, apiOutput := loadNestedArraysAPI(t, sdktest.AddCellOperation)
	sdkOutput := runInSDK(t, schema, apiOutput, nestedArraysService, "path_params_test.go", pathParamsSDKTest)
	if !sdkOutput.HasPathParams {
		t.Fatal("HasPathParams = false with grid.cell's path parameters")
	}
}

// pathParamsSDKTest runs in the generated SDK module against an httptest
// server that decodes the label segment once, as every server does, and
// answers with it in the success envelope.
const pathParamsSDKTest = `package sdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-nested-arrays-api"
)

const gridID = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"

func TestCellSendsEachLabelAsOneSegment(t *testing.T) {
	var requestURIs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURIs = append(requestURIs, r.RequestURI)
		var label any
		if segments := strings.Split(r.URL.EscapedPath(), "/"); len(segments) == 6 {
			decoded, err := url.PathUnescape(segments[5])
			if err != nil {
				t.Errorf("%s: %v", r.RequestURI, err)
			}
			label = decoded
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": label, "meta": map[string]string{"requestId": "req-1"}})
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ label, segment string }{
		{"%", "%25"},
		{"a%25b", "a%2525b"},
		{"100%", "100%25"},
		{"x%41y", "x%2541y"},
		{"a/b", "a%2Fb"},
		{"a b", "a%20b"},
		{"café", "caf%C3%A9"},
		{"a?b", "a%3Fb"},
		{"a#b", "a%23b"},
		// url.PathEscape leaves + as it is: in a path it is not a space.
		{"a+b", "a+b"},
	} {
		sent := len(requestURIs)
		got, err := client.GridNamespace.Cell(context.Background(), gridID, c.label)
		if err != nil {
			t.Errorf("Cell(%q): %v", c.label, err)
			continue
		}
		if len(requestURIs) != sent+1 {
			t.Fatalf("Cell(%q) sent %d requests", c.label, len(requestURIs)-sent)
		}
		if want := "/api/grids/" + gridID + "/cells/" + c.segment; requestURIs[sent] != want {
			t.Errorf("Cell(%q) requested %s, want %s", c.label, requestURIs[sent], want)
		}
		if got != c.label {
			t.Errorf("Cell(%q): the server received %q", c.label, got)
		}
	}
}
`
