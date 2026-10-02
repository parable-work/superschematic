package pysdkgen

import (
	"fmt"
	"slices"
	"testing"
)

// TestAMapArgumentIsADictOfItsValues: a map argument of body-args-api is
// typed as a dict from str to its value type, a list of the element type
// for a map of lists, and PyElementType is the type each value or element
// is validated as. The namespace gates the map helper and imports the
// value types.
func TestAMapArgumentIsADictOfItsValues(t *testing.T) {
	for method, want := range map[string][]string{
		"name_shades":  {"shade_by_name: dict[str, Shade]", "links_by_locale: dict[str, list[str]] | None = None"},
		"place_points": {"point_by_name: dict[str, Point]"},
	} {
		namespace, endpoint := bodyArgsEndpoint(t, method)
		for _, declaration := range want {
			if !hasMethodParam(endpoint, declaration) {
				t.Errorf("%s has no parameter %q: %+v", method, declaration, endpoint.MethodParams)
			}
		}
		for _, arg := range endpoint.ScalarArgs {
			if arg.PyName != "id" && (!arg.IsMap || arg.PyElementType == "") {
				t.Errorf("%s: %s IsMap %t, PyElementType %q; want a map with its value type", method, arg.PyName, arg.IsMap, arg.PyElementType)
			}
		}
		if !namespace.HasMapArgs || !slices.Contains(namespace.Imports, "Shade") || !slices.Contains(namespace.Imports, "Point") {
			t.Errorf("namespace %s HasMapArgs %t, imports %v; want the map helper, Shade and Point", namespace.Name, namespace.HasMapArgs, namespace.Imports)
		}
	}
}

// TestADELETESendsItsArgumentsInTheBody: the route reads the arguments of
// every method but GET from the JSON body (apigen.EndpointInfo.BodyArgs),
// so the SDK sends a DELETE's there, where a Generic.JSON one is any JSON
// value, and none in the query string.
func TestADELETESendsItsArgumentsInTheBody(t *testing.T) {
	_, endpoint := bodyArgsEndpoint(t, "remove_tags")
	if endpoint.HTTPMethod != "DELETE" || !endpoint.HasRequestBody || endpoint.HasQueryParams {
		t.Errorf("remove_tags: %s, HasRequestBody %t, HasQueryParams %t; want DELETE, true, false", endpoint.HTTPMethod, endpoint.HasRequestBody, endpoint.HasQueryParams)
	}
	for _, declaration := range []string{
		"labels: list[str]",
		"purge: bool | None = None",
		"reason: GenericJSON | None | Unset = UNSET",
	} {
		if !hasMethodParam(endpoint, declaration) {
			t.Errorf("remove_tags has no parameter %q: %+v", declaration, endpoint.MethodParams)
		}
	}
}

// TestMapAndDELETEArgumentsReachTheGoServer runs mapProbe and deleteProbe
// against the generated routes of body-args-api (writeBodyArgsModules):
// the Python SDK sends each map argument as the JSON object the route
// decodes, and refuses a value the route would refuse at the path the
// route names it by; and it sends a DELETE's arguments in the JSON body
// the route reads them from.
func TestMapAndDELETEArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	// The probes bind types_package, for the enum and the object type.
	typesPackage := fmt.Sprintf("types_package = __import__(%q)\n", modules.sdk.TypesPackage)
	t.Run("maps", func(t *testing.T) {
		modules.runPythonSDK(t, typesPackage+mapProbe, "", []map[string]string{
			{"id": `"p1"`, "shadeByName": `{"a": "light", "b": "dark"}`, "linksByLocale": `{"en": ["https://a.test"], "fr": []}`},
			{"id": `"p1"`, "shadeByName": `{}`, "linksByLocale": `null`},
			{"id": `"p1"`, "pointByName": `{"a": {"x": 1, "y": 2}, "b": {"x": 0.5, "y": -3}}`},
		})
	})
	t.Run("DELETE", func(t *testing.T) {
		modules.runPythonSDK(t, typesPackage+deleteProbe, "", []map[string]string{
			{"id": `"p1"`, "labels": `["c"]`, "purge": `false`, "reason": `null`},
			{"id": `"p1"`, "labels": `["a", "b"]`, "purge": `true`, "reason": `{"by": ["editor"]}`},
		})
	})
}

// mapProbe sends each map argument of nameShades and placePoints, an enum
// value as a member and as its serialized value, an object value as a
// model and as a dict. Then it checks that a value the route would refuse
// is refused at name[key], or name[key][i] for an element of a map of
// lists, every failure at once and before the request. A value outside the
// enum is invalid, as the SDK names it in a list.
const mapProbe = `
Shade, Point = types_package.Shade, types_package.Point

assert sdk.tag.name_shades("p1", {"a": Shade.Light, "b": "dark"}, links_by_locale={"en": ["https://a.test"], "fr": []}) is True
assert sdk.tag.name_shades("p1", {}) is True
assert sdk.tag.place_points("p1", {"a": Point(x=1, y=2), "b": {"x": 0.5, "y": -3}}) is True

for call, want in [
    (lambda: sdk.tag.name_shades("p1", None), {"shade_by_name": "required"}),
    (lambda: sdk.tag.name_shades("p1", ["light"]), {"shade_by_name": "type"}),
    (lambda: sdk.tag.name_shades("p1", {"a": "dim", "b": None, "c": Shade.Dark}), {"shade_by_name[a]": "invalid", "shade_by_name[b]": "required"}),
    (
        lambda: sdk.tag.name_shades("p1", {}, links_by_locale={"en": ["https://a.test", None, 5], "fr": "https://a.test", "de": None}),
        {"links_by_locale[en][1]": "required", "links_by_locale[en][2]": "type", "links_by_locale[fr]": "type", "links_by_locale[de]": "required"},
    ),
    (lambda: sdk.tag.place_points("p1", {"a": {"x": "far", "y": 0}, "b": None}), {"point_by_name[a].x": "type", "point_by_name[b]": "required"}),
]:
    try:
        call()
    except sdk_package.ValidationError as err:
        got = {path: errors[0]["validator"] for path, errors in err.errors.items()}
        assert got == want, (got, want)
    else:
        raise AssertionError(f"{want} was sent")
`

// deleteProbe calls removeTags, a DELETE, with the required argument and a
// null reason, then with every argument.
const deleteProbe = `
assert sdk.tag.remove_tags("p1", ["c"], reason=None) == ["c"]
assert sdk.tag.remove_tags("p1", ["a", "b"], purge=True, reason={"by": ["editor"]}) == ["a", "b"]
`
