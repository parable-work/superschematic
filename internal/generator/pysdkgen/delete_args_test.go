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

// TestDELETEArgumentsReachTheGoServer runs deleteArgsProbe against the
// generated routes of body-args-api (writeBodyArgsModules): the Python SDK
// sends removeTags's arguments, a string, a list, maps and JSON values, in
// the DELETE's body, and the implementation receives each as it was sent.
// A value the route would refuse is refused before the request.
func TestDELETEArgumentsReachTheGoServer(t *testing.T) {
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
	})
}

// deleteArgsProbe sends every argument of remove_tags, then remove_tags
// with audit set to None, which is the JSON null. Then it checks that a map
// value and a JSON object the route would refuse are refused before the
// request, at their paths.
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

for kwargs, field in [
    ({"shade_by_label": {"a": "dim"}}, "shade_by_label[a]"),
    ({"links_by_locale": {"en": ["https://a.test", None]}}, "links_by_locale[en][1]"),
    ({"notes": {"en": 1}}, "notes"),
]:
    try:
        sdk.tag.remove_tags("p1", "spam", ["a"], **kwargs)
    except sdk_package.ValidationError as err:
        assert list(err.errors) == [field], (kwargs, err.errors)
    else:
        raise AssertionError(f"{kwargs} was sent")
`
