package pysdkgen

import (
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestMapBodyArgumentsAreDicts: each map body argument of nameShades,
// placePoints and removeTags, a DELETE, is typed dict[str, T], and a map of
// lists dict[str, list[T]], as the Go route decodes it (bodyargs.Map,
// bodyargs.MapOfLists).
// PyElementType is T, the type each value, or each element of a value, is
// validated as. The namespace gates _validate_map_argument.
func TestMapBodyArgumentsAreDicts(t *testing.T) {
	for _, tc := range []struct {
		method, name, pyType, element, declaration string
	}{
		{"name_shades", "shade_by_name", "dict[str, Shade]", "Shade", "shade_by_name: dict[str, Shade]"},
		{"name_shades", "links_by_locale", "dict[str, list[str]]", "str", "links_by_locale: dict[str, list[str]] | None = None"},
		{"place_points", "point_by_name", "dict[str, Point]", "Point", "point_by_name: dict[str, Point]"},
		{"remove_tags", "shade_by_label", "dict[str, Shade]", "Shade", "shade_by_label: dict[str, Shade] | None = None"},
	} {
		namespace, endpoint := bodyArgsEndpoint(t, tc.method)
		if !namespace.HasMapArgs {
			t.Errorf("namespace %s: HasMapArgs is false", namespace.Name)
		}
		i := slices.IndexFunc(endpoint.ScalarArgs, func(arg ScalarArg) bool { return arg.PyName == tc.name })
		if i < 0 {
			t.Fatalf("%s has no argument %s", tc.method, tc.name)
		}
		if arg := endpoint.ScalarArgs[i]; !arg.IsMap || arg.PyType != tc.pyType || arg.PyElementType != tc.element {
			t.Errorf("%s %s: IsMap %t, type %s, element type %s; want true, %s, %s", tc.method, tc.name, arg.IsMap, arg.PyType, arg.PyElementType, tc.pyType, tc.element)
		}
		if !hasMethodParam(endpoint, tc.declaration) {
			t.Errorf("%s has no parameter %q: %+v", tc.method, tc.declaration, endpoint.MethodParams)
		}
	}
}

// TestAMapOfJSONValuesIsADictOfTheAlias: a map of a JSON-valued scalar is a
// dict of the types package's alias, which the namespace imports and
// _JSON_VALUE_TYPES holds, so None as a value is refused. An optional map
// of Generic.JSON keeps no null: the route has KeepNull only for a single
// value, and None leaves the map out.
func TestAMapOfJSONValuesIsADictOfTheAlias(t *testing.T) {
	for _, tc := range []struct {
		arg     apigen.BodyArg
		pyType  string
		element string
	}{
		{apigen.BodyArg{Param: apigen.Param{Name: "settings", Type: "Generic.JSON", IsMap: true}, Kind: "Any", AnyJSON: true}, "dict[str, GenericJSON]", "GenericJSON"},
		{apigen.BodyArg{Param: apigen.Param{Name: "labelsByPost", Type: "Generic.StringMap", IsMap: true, IsArray: true}, Kind: "Object", StructuredJSON: "object"}, "dict[str, list[GenericStringMap]]", "GenericStringMap"},
	} {
		endpoint := apigen.EndpointInfo{
			Name: "preview", Namespace: "preview", Path: "/api/previews", Method: "PUT",
			ScalarArgs: []apigen.Param{tc.arg.Param},
			BodyArgs:   []apigen.BodyArg{tc.arg},
		}
		sdk, err := Generate(&apigen.APIOutput{SchemaName: "previews", Endpoints: []apigen.EndpointInfo{endpoint}}, "", "", nestedArraysClock)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		namespace := sdk.Namespaces[0]
		arg := namespace.Endpoints[0].ScalarArgs[0]
		if !arg.IsMap || arg.PyType != tc.pyType || arg.PyElementType != tc.element || !arg.isJSONValue() || arg.KeepsNull {
			t.Errorf("%s: IsMap %t, type %s, element type %s, JSON value %t, KeepsNull %t; want true, %s, %s, true, false", tc.arg.Name, arg.IsMap, arg.PyType, arg.PyElementType, arg.isJSONValue(), arg.KeepsNull, tc.pyType, tc.element)
		}
		if !slices.Contains(namespace.Imports, tc.element) || !slices.Equal(namespace.JSONValueTypes, []string{tc.element}) {
			t.Errorf("%s: the namespace imports %v, JSONValueTypes %v; want %s in both", tc.arg.Name, namespace.Imports, namespace.JSONValueTypes, tc.element)
		}
	}
}

// TestMapArgumentsReachTheGoServer runs mapArgsProbe against the generated
// routes of body-args-api (writeBodyArgsModules): the Python SDK sends each
// map argument of nameShades, placePoints and removeTags, a DELETE, as a
// JSON object of its validated values in the body, and the implementation
// receives each map, an empty one as empty and an absent optional one as
// nil. A map the route would refuse
// is refused before the request, every failure at once, each at the path
// the route reports it: the argument, name[key], name[key][i] or
// name[key].field.
func TestMapArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	modules.runPythonSDK(t, mapArgsProbe, "", []map[string]string{
		{"id": `"p1"`, "shadeByName": `{"a": "light", "b": "dark"}`, "linksByLocale": `{"en": ["https://a.test"], "fr": []}`},
		{"id": `"p2"`, "shadeByName": `{}`, "linksByLocale": `null`},
		{"id": `"p3"`, "shadeByName": `{"a": "light"}`, "linksByLocale": `{"en": ["https://b.test"]}`},
		{"id": `"p4"`, "pointByName": `{"a": {"x": 1, "y": 2}, "b": {"x": 0.5, "y": -1}}`},
		{"id": `"p5"`, "labels": `["a", "b"]`, "shadeByLabel": `{"a": "dark", "b": "light"}`},
		{"id": `"p5"`, "labels": `["c"]`, "shadeByLabel": `{}`},
		{"id": `"p5"`, "labels": `["d"]`, "shadeByLabel": `null`},
	})
}

// mapArgsProbe sends maps through name_shades, place_points and
// remove_tags: of enum values and members, of lists, empty, absent, a
// read-only mapping with a tuple, and of Point dicts and models. Then it checks that maps the route
// would refuse are refused before the request with the errors it expects,
// by path; a validator of None is not checked.
const mapArgsProbe = `
from types import MappingProxyType

Shade, Point = types_package.Shade, types_package.Point

assert sdk.tag.name_shades("p1", {"a": "light", "b": Shade.Dark}, links_by_locale={"en": ["https://a.test"], "fr": []}) is True
assert sdk.tag.name_shades("p2", {}) is True
assert sdk.tag.name_shades("p3", MappingProxyType({"a": Shade.Light}), links_by_locale=MappingProxyType({"en": ("https://b.test",)})) is True
assert sdk.tag.place_points("p4", {"a": {"x": 1, "y": 2}, "b": Point(x=0.5, y=-1)}) is True
assert sdk.tag.remove_tags("p5", ["a", "b"], shade_by_label={"a": "dark", "b": Shade.Light}) == ["a", "b"]
assert sdk.tag.remove_tags("p5", ["c"], shade_by_label={}) == ["c"]
assert sdk.tag.remove_tags("p5", ["d"]) == ["d"]

for method, kwargs, want in [
    (sdk.tag.name_shades, {"shade_by_name": None}, {"shade_by_name": "required"}),
    (sdk.tag.name_shades, {"shade_by_name": "light"}, {"shade_by_name": "type"}),
    (sdk.tag.name_shades, {"shade_by_name": ["light"]}, {"shade_by_name": "type"}),
    (sdk.tag.name_shades, {"shade_by_name": {1: "light"}}, {"shade_by_name": "type"}),
    (
        sdk.tag.name_shades,
        {"shade_by_name": {"a": "dim", "b": None, "c": 5}},
        {"shade_by_name[a]": None, "shade_by_name[b]": "required", "shade_by_name[c]": None},
    ),
    (sdk.tag.name_shades, {"shade_by_name": {}, "links_by_locale": ["https://a.test"]}, {"links_by_locale": "type"}),
    (
        sdk.tag.name_shades,
        {"shade_by_name": {}, "links_by_locale": {"en": ["https://a.test", None, 5], "fr": "https://a.test", "de": None}},
        {
            "links_by_locale[en][1]": "required",
            "links_by_locale[en][2]": "type",
            "links_by_locale[fr]": "type",
            "links_by_locale[de]": "required",
        },
    ),
    (
        sdk.tag.place_points,
        {"point_by_name": {"a": {"x": "one", "y": 0}, "b": None}},
        {"point_by_name[a].x": "type", "point_by_name[b]": "required"},
    ),
    # A value's own rules, by wire name under its path.
    (
        sdk.tag.place_points,
        {"point_by_name": {"a": {"x": -1, "y": 0, "pinLabel": "a"}}},
        {"point_by_name[a].x": "min", "point_by_name[a].pinLabel": "minLength"},
    ),
    # The map is the argument, not a Point: each of its values is one.
    (sdk.tag.place_points, {"point_by_name": {"x": 1, "y": 2}}, {"point_by_name[x]": "type", "point_by_name[y]": "type"}),
    (sdk.tag.remove_tags, {"labels": ["a"], "shade_by_label": "dark"}, {"shade_by_label": "type"}),
    (
        sdk.tag.remove_tags,
        {"labels": ["a"], "shade_by_label": {"a": "dim", "b": None}},
        {"shade_by_label[a]": None, "shade_by_label[b]": "required"},
    ),
]:
    try:
        method("p0", **kwargs)
    except sdk_package.ValidationError as err:
        assert sorted(err.errors) == sorted(want), (kwargs, err.errors)
        for field, validator in want.items():
            if validator is not None:
                assert [e["validator"] for e in err.errors[field]] == [validator], (kwargs, err.errors)
    else:
        raise AssertionError(f"{kwargs} was sent")
`
