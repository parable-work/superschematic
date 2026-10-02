package pysdkgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestOnlyAGETSendsItsArgumentsInTheQueryString: the route reads the
// scalar arguments of a GET from the query string and those of every other
// method from the JSON body, so the SDK sends them there.
func TestOnlyAGETSendsItsArgumentsInTheQueryString(t *testing.T) {
	param := apigen.Param{Name: "reason", Type: "string", IsString: true, Required: true}
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		endpoint := apigen.EndpointInfo{Path: "/api/posts", Method: method, ScalarArgs: []apigen.Param{param}}
		if got := convertEndpoint(endpoint, false, "").HasRequestBody; got != (method != "GET") {
			t.Errorf("%s: HasRequestBody = %t, want %t", method, got, method != "GET")
		}
	}
}

// TestADELETEArgumentIsTypedAsItsBodyValue: removeTags is a DELETE, so
// each JSON-valued argument is typed as its JSON value and each map as a
// dict, as a POST's are.
func TestADELETEArgumentIsTypedAsItsBodyValue(t *testing.T) {
	_, endpoint := bodyArgsEndpoint(t, "remove_tags")
	if endpoint.HTTPMethod != "DELETE" || !endpoint.HasRequestBody {
		t.Fatalf("remove_tags: method %s, HasRequestBody %t; want DELETE, true", endpoint.HTTPMethod, endpoint.HasRequestBody)
	}
	for _, declaration := range []string{
		"reason: str",
		"labels: list[str]",
		"shade_by_label: dict[str, Shade] | None = None",
		"links_by_locale: dict[str, list[str]] | None = None",
		"audit: GenericJSON | None | Unset = UNSET",
		"notes: GenericStringMap | None = None",
		"vector: EmbeddingVector | None = None",
	} {
		if !hasMethodParam(endpoint, declaration) {
			t.Errorf("remove_tags has no parameter %q: %+v", declaration, endpoint.MethodParams)
		}
	}
}

// TestDELETEAndMapArgumentsReachTheGoServer runs deleteArgsProbe against
// the generated routes of body-args-api (writeBodyArgsModules): the Python
// SDK sends removeTags's arguments, a string, a list, maps and JSON values,
// in the DELETE's body, and the maps of nameShades and placePoints, and the
// implementation receives each as it was sent. A map value the route would
// refuse is refused before the request, at its key.
func TestDELETEAndMapArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	modules.runPythonSDK(t, deleteArgsProbe, "", []map[string]string{
		{
			"id":            `"p1"`,
			"reason":        `"spam"`,
			"labels":        `["a", "b"]`,
			"shadeByLabel":  `{"a": "dark"}`,
			"linksByLocale": `{"en": ["https://a.test"], "fr": []}`,
			"audit":         `{"by": [1]}`,
			"notes":         `{"en": "gone"}`,
			"vector":        `[0.5, -1]`,
		},
		{"reason": `"spam"`, "labels": `[]`, "shadeByLabel": `null`, "audit": `null`, "notes": `null`},
		{"id": `"p1"`, "shadeByName": `{"a": "light", "b": "dark"}`, "linksByLocale": `{"en": ["https://a.test"]}`},
		{"id": `"p1"`, "pointByName": `{"a": {"x": 1, "y": 2}}`},
	})
}

// deleteArgsProbe sends every argument of remove_tags, then remove_tags
// with audit set to None, which is the JSON null, then the maps of
// name_shades and place_points. Then it checks that map values the route
// would refuse are refused before the request, each with one error at the
// value's path, or at the path of a field of a Point value.
const deleteArgsProbe = `
got = sdk.tag.remove_tags(
    "p1",
    "spam",
    ["a", "b"],
    shade_by_label={"a": "dark"},
    links_by_locale={"en": ["https://a.test"], "fr": []},
    audit={"by": [1]},
    notes={"en": "gone"},
    vector=[0.5, -1],
)
assert got == ["a", "b"], got
assert sdk.tag.remove_tags("p1", "spam", [], audit=None) == []
assert sdk.tag.name_shades("p1", {"a": "light", "b": "dark"}, links_by_locale={"en": ["https://a.test"]}) is True
assert sdk.tag.place_points("p1", {"a": {"x": 1, "y": 2}}) is True

for kwargs, field, validator in [
    ({"shade_by_label": "dark"}, "shade_by_label", "type"),
    ({"shade_by_label": {"a": "dim"}}, "shade_by_label[a]", None),
    ({"shade_by_label": {"a": None}}, "shade_by_label[a]", "required"),
    ({"links_by_locale": {"en": None}}, "links_by_locale[en]", "required"),
    ({"links_by_locale": {"en": "https://a.test"}}, "links_by_locale[en]", "type"),
    ({"links_by_locale": {"en": ["https://a.test", None]}}, "links_by_locale[en][1]", "required"),
    ({"notes": {"en": 1}}, "notes", None),
]:
    try:
        sdk.tag.remove_tags("p1", "spam", ["a"], **kwargs)
    except sdk_package.ValidationError as err:
        assert list(err.errors) == [field], (kwargs, err.errors)
        if validator is not None:
            assert [e["validator"] for e in err.errors[field]] == [validator], (kwargs, err.errors)
    else:
        raise AssertionError(f"{kwargs} was sent")

for point_by_name, field in [
    ({"a": 5}, "point_by_name[a]"),
    ({"a": None}, "point_by_name[a]"),
    ({"a": {"x": 1, "y": 2}, "b": {"y": 0}}, "point_by_name[b].x"),
]:
    try:
        sdk.tag.place_points("p1", point_by_name)
    except sdk_package.ValidationError as err:
        assert list(err.errors) == [field], (point_by_name, err.errors)
    else:
        raise AssertionError(f"{point_by_name} was sent")
`
