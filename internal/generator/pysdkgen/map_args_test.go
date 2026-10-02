package pysdkgen

import (
	"fmt"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestAMapBodyArgumentIsADict: a map body argument (Record<string, T>) of
// nameShades and placePoints is typed dict[str, T], and a map of lists
// (Record<string, T[]>) dict[str, list[T]], the JSON object the Go route
// reads. PyElementType is T, as which each value, or each element of a
// list value, is validated. The namespace imports an enum or object type
// T and has the map helpers.
func TestAMapBodyArgumentIsADict(t *testing.T) {
	for method, args := range map[string]map[string]struct{ pyType, elementType, declaration string }{
		"name_shades": {
			"shade_by_name":   {"dict[str, Shade]", "Shade", "shade_by_name: dict[str, Shade]"},
			"links_by_locale": {"dict[str, list[str]]", "str", "links_by_locale: dict[str, list[str]] | None = None"},
		},
		"place_points": {
			"point_by_name": {"dict[str, Point]", "Point", "point_by_name: dict[str, Point]"},
		},
	} {
		namespace, endpoint := bodyArgsEndpoint(t, method)
		for _, arg := range endpoint.ScalarArgs {
			want, ok := args[arg.PyName]
			if !ok {
				continue
			}
			delete(args, arg.PyName)
			if arg.PyType != want.pyType || arg.PyElementType != want.elementType || !arg.IsMap {
				t.Errorf("%s %s: type %s, element type %s, IsMap %t; want %s, %s, true", method, arg.PyName, arg.PyType, arg.PyElementType, arg.IsMap, want.pyType, want.elementType)
			}
			if !hasMethodParam(endpoint, want.declaration) {
				t.Errorf("%s has no parameter %q: %+v", method, want.declaration, endpoint.MethodParams)
			}
		}
		for name := range args {
			t.Errorf("%s has no argument %s", method, name)
		}
		if !namespace.HasMapArgs {
			t.Errorf("namespace %s has no map helpers", namespace.Name)
		}
		for _, symbol := range []string{"Shade", "Point"} {
			if !slices.Contains(namespace.Imports, symbol) {
				t.Errorf("namespace %s imports %v, want %s", namespace.Name, namespace.Imports, symbol)
			}
		}
	}
}

// TestAMapOfJSONValuesIsADictOfTheirAlias: a map of a JSON-valued scalar,
// alone or of lists, is a dict of the types package's alias of the scalar,
// which the namespace imports and _JSON_VALUE_TYPES holds, so a None value
// is refused as required. A map is never a KeepsNull argument.
func TestAMapOfJSONValuesIsADictOfTheirAlias(t *testing.T) {
	document := apigen.Param{Name: "documentByName", Type: "Generic.JSON", IsMap: true, Required: true}
	labels := apigen.Param{Name: "labelsByLocale", Type: "Generic.StringMap", IsMap: true, IsArray: true}
	endpoint := apigen.EndpointInfo{
		Name: "storeDocuments", Namespace: "document", Path: "/api/documents", Method: "PUT",
		ScalarArgs: []apigen.Param{document, labels},
		BodyArgs: []apigen.BodyArg{
			{Param: document, Kind: "Any", AnyJSON: true},
			{Param: labels, Kind: "Object", StructuredJSON: "object"},
		},
	}
	sdk, err := Generate(&apigen.APIOutput{SchemaName: "documents", Endpoints: []apigen.EndpointInfo{endpoint}}, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	namespace := sdk.Namespaces[0]
	want := map[string]string{
		"document_by_name": "dict[str, GenericJSON]",
		"labels_by_locale": "dict[str, list[GenericStringMap]]",
	}
	for _, arg := range namespace.Endpoints[0].ScalarArgs {
		if arg.PyType != want[arg.PyName] || !arg.IsMap || !arg.isJSONValue() || arg.KeepsNull {
			t.Errorf("%s: type %s, IsMap %t, JSON value %t, KeepsNull %t; want %s, true, true, false", arg.PyName, arg.PyType, arg.IsMap, arg.isJSONValue(), arg.KeepsNull, want[arg.PyName])
		}
	}
	if aliases := []string{"GenericJSON", "GenericStringMap"}; !slices.Equal(namespace.Imports, aliases) || !slices.Equal(namespace.JSONValueTypes, aliases) {
		t.Errorf("the namespace imports %v, JSONValueTypes %v; want %v for both", namespace.Imports, namespace.JSONValueTypes, aliases)
	}
}

// TestMapArgumentsReachTheGoServer runs mapArgsProbe against the generated
// routes of body-args-api (writeBodyArgsModules): the Python SDK sends each
// map argument of nameShades and placePoints as a JSON object, and the
// implementation receives each value as sent, an enum member as its
// serialized value and a Point model as its fields. An absent optional map
// reaches it as nil. A map the route would refuse is refused before the
// request, at the argument, at name[key] or at name[key][i].
func TestMapArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	want := []map[string]string{
		{"id": `"p1"`, "shadeByName": `{"a": "light", "b": "dark"}`, "linksByLocale": `{"en": ["https://a.test", "https://b.test"], "fr": []}`},
		{"id": `"p2"`, "shadeByName": `{}`, "linksByLocale": `null`},
		{"id": `"p3"`, "shadeByName": `{"a": "dark"}`, "linksByLocale": `{}`},
		{"id": `"p4"`, "pointByName": `{"a": {"x": 1, "y": 2}, "b": {"x": 0, "y": -2.5}}`},
		{"id": `"p5"`, "pointByName": `{"a": {"x": 3, "y": 4}}`},
	}
	modules.runPythonSDK(t, fmt.Sprintf(mapArgsProbe, modules.sdk.TypesPackage), "null", want)
}

// mapArgsProbe sends maps through name_shades and place_points, then checks
// that maps the route would refuse are refused before the request, each
// failure at its path: the argument when it is no dict or has a key that
// is no str, name[key] for a value, name[key][i] for an element of a list
// value, and name[key].field inside a Point. It is formatted with the
// types package.
const mapArgsProbe = `
types_package = __import__(%[1]q)

assert sdk.tag.name_shades("p1", {"a": "light", "b": "dark"}, links_by_locale={"en": ["https://a.test", "https://b.test"], "fr": []}) is True
assert sdk.tag.name_shades("p2", {}) is True
assert sdk.tag.name_shades("p3", {"a": types_package.Shade.Dark}, links_by_locale={}) is True
assert sdk.tag.place_points("p4", {"a": {"x": 1, "y": 2}, "b": {"x": 0, "y": -2.5}}) is True
assert sdk.tag.place_points("p5", {"a": types_package.Point(x=3, y=4)}) is True

for method, args, kwargs, want in [
    ("name_shades", ("p", None), {}, {"shade_by_name": "required"}),
    ("name_shades", ("p", "light"), {}, {"shade_by_name": "type"}),
    ("name_shades", ("p", ["light"]), {}, {"shade_by_name": "type"}),
    ("name_shades", ("p", {1: "light"}), {}, {"shade_by_name": "type"}),
    ("name_shades", ("p", {"a": "dim", "b": None, "c": 5}), {}, {"shade_by_name[a]": None, "shade_by_name[b]": "required", "shade_by_name[c]": None}),
    ("name_shades", ("p", {}), {"links_by_locale": ["https://a.test"]}, {"links_by_locale": "type"}),
    (
        "name_shades",
        ("p", {}),
        {"links_by_locale": {"en": ["https://a.test", None], "fr": "https://a.test", "de": None, "es": [5]}},
        {"links_by_locale[en][1]": "required", "links_by_locale[fr]": "type", "links_by_locale[de]": "required", "links_by_locale[es][0]": "type"},
    ),
    ("place_points", ("p", None), {}, {"point_by_name": "required"}),
    ("place_points", ("p", {"x": 1, "y": 2}), {}, {"point_by_name[x]": None, "point_by_name[y]": None}),
    ("place_points", ("p", {"a": {"x": 1}, "b": None}), {}, {"point_by_name[a].y": "required", "point_by_name[b]": "required"}),
]:
    try:
        getattr(sdk.tag, method)(*args, **kwargs)
    except sdk_package.ValidationError as err:
        assert sorted(err.errors) == sorted(want), (args, kwargs, err.errors)
        for field, validator in want.items():
            if validator is not None:
                assert [e["validator"] for e in err.errors[field]] == [validator], (args, kwargs, err.errors)
    else:
        raise AssertionError(f"{method}{args} {kwargs} was sent")
`
