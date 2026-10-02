package sdkgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestMapArgumentsAreCheckedValueByValue pins the checks of a map body
// argument: each value, or each element of a list value, is validated at
// name[key] or name[key][i] with the Required validator of its type,
// imported for an optional map too, and the map is never checked as one
// value or iterated as a list.
func TestMapArgumentsAreCheckedValueByValue(t *testing.T) {
	sdkOutput, err := Generate(&apigen.APIOutput{
		SchemaName: "shop-api",
		Endpoints: []apigen.EndpointInfo{{
			Name:       "nameShades",
			Namespace:  "tag",
			Method:     "PUT",
			Path:       "/api/shade-names",
			OutputType: "boolean",
			ScalarArgs: []apigen.ScalarArg{
				{Name: "shadeByName", Type: "Shade", IsMap: true},
				{Name: "linksByLocale", Type: "Network.Url", IsMap: true, IsArray: true},
				{Name: "weightByName", Type: "number", IsMap: true, Required: true},
			},
		}},
	}, nil, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	namespace := sdkOutput.Namespaces[0]
	for _, symbol := range []string{"validateShadeRequired", "validateNetworkUrlRequired"} {
		if !slices.Contains(namespace.Imports, symbol) {
			t.Errorf("imports %v miss %s", namespace.Imports, symbol)
		}
	}
	got := renderNamespace(t, namespace.Endpoints[0])
	for _, snippet := range []string{
		"validateShadeRequired(value as Shade)",
		"checkValue(value, `shadeByName[${key}]`)",
		"validateNetworkUrlRequired(value as NetworkUrl)",
		"setFieldErrors(errors, `linksByLocale[${key}]`, [{ validator: \"type\", message: \"expected an array\" }])",
		"checkValue(elem, `linksByLocale[${key}][${index}]`)",
		"setFieldErrors(errors, \"weightByName\", [{ validator: \"required\", message: \"weightByName is required\" }])",
		"checkValue(value, `weightByName[${key}]`)",
	} {
		if !strings.Contains(got, snippet) {
			t.Errorf("namespace output is missing %s", snippet)
		}
	}
	for _, snippet := range []string{"validateShadeRequired(scalarInput.", "of (scalarInput.linksByLocale"} {
		if strings.Contains(got, snippet) {
			t.Errorf("namespace output checks a map as one value or a list: %s", snippet)
		}
	}
	if t.Failed() {
		t.Logf("generated:\n%s", got)
	}
}

// TestMapArgsSDKSendsObjectsAndChecksEachValue runs test_map_args.js on the
// TypeScript SDK of body-args-api (runBodyArgsSDKScript): nameShades,
// placePoints and removeTags send each map argument as the JSON object the
// route reads, {} included, and a map that is not an object, a null value,
// a list value that is not an array or a value that fails its own
// validation is refused at name, name[key] or name[key][i] before any
// request, as bodyargs.Map and bodyargs.MapOfLists refuse them.
func TestMapArgsSDKSendsObjectsAndChecksEachValue(t *testing.T) {
	runBodyArgsSDKScript(t, "test_map_args.js")
}
