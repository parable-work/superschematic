package pysdkgen

import (
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
)

// bodyArgsEndpoint returns the namespace and the endpoint of body-args-api's
// Python SDK whose method is methodName.
func bodyArgsEndpoint(t *testing.T, methodName string) (NamespaceInfo, EndpointInfo) {
	t.Helper()
	apiOutput, err := apigen.Generate(loadBodyArgsAPI(t), apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: bodyArgsAPI,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdk, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, namespace := range sdk.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			if endpoint.MethodName == methodName {
				return namespace, endpoint
			}
		}
	}
	t.Fatalf("body-args-api has no %s", methodName)
	return NamespaceInfo{}, EndpointInfo{}
}

// TestJSONObjectAndArrayBodyArgumentsAreTheirTypes: each JSON object or
// array scalar body argument of storeEmbedding is typed as the types
// package's alias of its scalar, the type pygen gives a field of it
// (GenericStringMap, EmbeddingVector), alone, in a list and in a list of
// lists. The namespace imports each alias, and _JSON_VALUE_TYPES holds
// them with GenericJSON.
func TestJSONObjectAndArrayBodyArgumentsAreTheirTypes(t *testing.T) {
	namespace, endpoint := bodyArgsEndpoint(t, "store_embedding")
	want := map[string]string{
		"labels":      "GenericStringMap",
		"vector":      "EmbeddingVector",
		"label_sets":  "list[GenericStringMap]",
		"vector_grid": "list[list[EmbeddingVector]]",
	}
	for _, arg := range endpoint.ScalarArgs {
		if arg.PyType != want[arg.PyName] || !arg.IsStructuredJSON || arg.IsAnyJSON {
			t.Errorf("%s: type %s, IsStructuredJSON %t, IsAnyJSON %t; want %s, true, false", arg.PyName, arg.PyType, arg.IsStructuredJSON, arg.IsAnyJSON, want[arg.PyName])
		}
	}
	for _, declaration := range []string{
		"labels: GenericStringMap",
		"vector: EmbeddingVector | None = None",
		"label_sets: list[GenericStringMap] | None = None",
		"vector_grid: list[list[EmbeddingVector]] | None = None",
	} {
		if !hasMethodParam(endpoint, declaration) {
			t.Errorf("store_embedding has no parameter %q: %+v", declaration, endpoint.MethodParams)
		}
	}
	for _, symbol := range []string{"GenericStringMap", "EmbeddingVector"} {
		if !slices.Contains(namespace.Imports, symbol) {
			t.Errorf("namespace %s imports %v, want %s", namespace.Name, namespace.Imports, symbol)
		}
	}
	if want := []string{"EmbeddingVector", "GenericJSON", "GenericStringMap"}; !slices.Equal(namespace.JSONValueTypes, want) {
		t.Errorf("namespace %s JSONValueTypes = %v, want %v", namespace.Name, namespace.JSONValueTypes, want)
	}
}

// TestAJSONObjectOrArrayInTheQueryStringStaysAStr: a GET sends its scalar
// arguments in the query string, where a JSON object or array scalar
// argument stays a str, its JSON text, and the same argument of a POST is
// the types package's alias.
func TestAJSONObjectOrArrayInTheQueryStringStaysAStr(t *testing.T) {
	for _, scalar := range []struct{ typeName, kind, structured, alias string }{
		{"Generic.StringMap", "Object", "object", "GenericStringMap"},
		{"Embedding.Vector", "Array", "array", "EmbeddingVector"},
	} {
		param := apigen.Param{Name: "filter", Type: scalar.typeName, Required: true}
		for method, want := range map[string]string{"GET": "str", "POST": scalar.alias} {
			endpoint := apigen.EndpointInfo{Path: "/api/previews", Method: method, ScalarArgs: []apigen.Param{param}}
			if method != "GET" {
				endpoint.BodyArgs = []apigen.BodyArg{{Param: param, Kind: scalar.kind, StructuredJSON: scalar.structured}}
			}
			if got := convertEndpoint(endpoint, false, "").ScalarArgs[0]; got.PyType != want || got.IsStructuredJSON != (method != "GET") {
				t.Errorf("%s %s: type %s, IsStructuredJSON %t; want %s", method, scalar.typeName, got.PyType, got.IsStructuredJSON, want)
			}
		}
	}
}

// TestAJSONScalarResponseIsTypedAsItsAlias: an operation that returns a
// JSON-valued scalar, alone or in a list, returns the types package's alias
// of it, which the namespace imports although no argument uses it. The
// response is the JSON value as decoded, so OutputModelName stays empty and
// no model coerces it.
func TestAJSONScalarResponseIsTypedAsItsAlias(t *testing.T) {
	for method, want := range map[string]string{
		"store_document":  "GenericJSON",
		"revise_document": "GenericJSON",
		"store_embedding": "GenericStringMap",
	} {
		namespace, endpoint := bodyArgsEndpoint(t, method)
		if endpoint.OutputTypeHint != want || endpoint.OutputModelName != "" {
			t.Errorf("%s returns %s, model %q; want %s, no model", method, endpoint.OutputTypeHint, endpoint.OutputModelName, want)
		}
		if !slices.Contains(namespace.Imports, want) {
			t.Errorf("namespace %s imports %v, want %s", namespace.Name, namespace.Imports, want)
		}
	}
	for _, tc := range []struct {
		endpoint   apigen.EndpointInfo
		hint, name string
	}{
		{apigen.EndpointInfo{OutputType: "Generic.JSON", OutputAnyJSON: true}, "GenericJSON", "GenericJSON"},
		{apigen.EndpointInfo{OutputType: "Generic.JSON", OutputAnyJSON: true, OutputIsArray: true}, "list[GenericJSON]", "GenericJSON"},
		{apigen.EndpointInfo{OutputType: "Generic.StringMap", OutputStructuredJSON: "object", OutputIsArray: true}, "list[GenericStringMap]", "GenericStringMap"},
		{apigen.EndpointInfo{OutputType: "Embedding.Vector", OutputStructuredJSON: "array", OutputIsArray: true, OutputIsArrayOfArrays: true}, "list[list[EmbeddingVector]]", "EmbeddingVector"},
	} {
		tc.endpoint.Name, tc.endpoint.Namespace, tc.endpoint.Path, tc.endpoint.Method = "preview", "preview", "/api/previews", "GET"
		sdk, err := Generate(&apigen.APIOutput{SchemaName: "previews", Endpoints: []apigen.EndpointInfo{tc.endpoint}}, "", "", nestedArraysClock)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		namespace := sdk.Namespaces[0]
		if endpoint := namespace.Endpoints[0]; endpoint.OutputTypeHint != tc.hint || endpoint.OutputModelName != "" {
			t.Errorf("%s returns %s, model %q; want %s, no model", tc.hint, endpoint.OutputTypeHint, endpoint.OutputModelName, tc.hint)
		}
		if !slices.Equal(namespace.Imports, []string{tc.name}) {
			t.Errorf("%s: the namespace imports %v, want [%s]", tc.hint, namespace.Imports, tc.name)
		}
	}
}

// TestJSONObjectAndArrayArgumentsReachTheGoServer runs
// structuredJSONProbe against the generated routes of body-args-api
// (writeBodyArgsModules): the Python SDK sends a JSON object as each
// Generic.StringMap argument of storeEmbedding and a JSON array as each
// Embedding.Vector one, alone, in a list and in a list of lists, and the
// implementation receives each as that object or array. JSON text is read
// into the value it holds and sent as that value. A value the route would
// refuse is refused before the request, at the argument's path.
func TestJSONObjectAndArrayArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	var want []map[string]string
	for _, value := range structuredJSONValues {
		want = append(want, map[string]string{
			"labels":     value.labels,
			"vector":     value.vector,
			"labelSets":  "[" + value.labels + ", {}]",
			"vectorGrid": "[[" + value.vector + "], []]",
		})
	}
	want = append(want, map[string]string{
		"labels":     `{"en": "Hi"}`,
		"vector":     `[1, 2.5]`,
		"labelSets":  `[{"en": "Hi"}]`,
		"vectorGrid": `[[[1, 2.5]]]`,
	})
	arg := "["
	for i, value := range structuredJSONValues {
		if i > 0 {
			arg += ","
		}
		arg += "[" + value.labels + "," + value.vector + "]"
	}
	modules.runPythonSDK(t, structuredJSONProbe, arg+"]", want)
}

// structuredJSONValues are what the Python SDK sends as the Generic.StringMap
// and Embedding.Vector arguments of storeEmbedding, one call each.
var structuredJSONValues = []struct{ labels, vector string }{
	{`{"en": "Hello", "fr": "Bonjour"}`, `[0.5, -1, 0]`},
	{`{}`, `[]`},
}

// structuredJSONProbe takes a JSON array of [labels, vector] pairs and
// sends each through store_embedding, alone, in a list and in a list of
// lists, then the same as JSON text. Then it checks that values the route
// would refuse are refused before the request, each with one error at the
// argument's path; None where null is not a value is required.
const structuredJSONProbe = `
for labels, vector in json.loads(sys.argv[2]):
    got = sdk.tag.store_embedding(labels, vector=vector, label_sets=[labels, {}], vector_grid=[[vector], []])
    assert got == labels, (labels, got)

# JSON text is read into the object or array it holds, as the types
# package reads it, and sent as that value.
got = sdk.tag.store_embedding('{"en": "Hi"}', vector="[1, 2.5]", label_sets=['{"en": "Hi"}'], vector_grid=[["[1, 2.5]"]])
assert got == {"en": "Hi"}, got

for kwargs, field, validator in [
    ({"labels": None}, "labels", "required"),
    ({"labels": ["en"]}, "labels", None),
    ({"labels": "[]"}, "labels", None),
    ({"labels": {"en": 1}}, "labels", None),
    ({"labels": {}, "vector": {"x": 1}}, "vector", None),
    ({"labels": {}, "vector": [1, True]}, "vector", None),
    ({"labels": {}, "vector": "not JSON"}, "vector", None),
    ({"labels": {}, "label_sets": [None]}, "label_sets[0]", "required"),
    ({"labels": {}, "label_sets": [["en"]]}, "label_sets[0]", None),
    ({"labels": {}, "vector_grid": [[None]]}, "vector_grid[0][0]", "required"),
    ({"labels": {}, "vector_grid": [[{"x": 1}]]}, "vector_grid[0][0]", None),
]:
    try:
        sdk.tag.store_embedding(**kwargs)
    except sdk_package.ValidationError as err:
        assert list(err.errors) == [field], (kwargs, err.errors)
        if validator is not None:
            assert [e["validator"] for e in err.errors[field]] == [validator], (kwargs, err.errors)
    else:
        raise AssertionError(f"{kwargs} was sent")
`
