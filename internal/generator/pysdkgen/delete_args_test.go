package pysdkgen

import "testing"

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

// TestDELETEArgumentsReachTheGoServer runs deleteProbe against the
// generated routes of body-args-api (writeBodyArgsModules): the Python SDK
// sends a DELETE's arguments in the JSON body the route reads them from.
func TestDELETEArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	modules.runPythonSDK(t, deleteProbe, "", []map[string]string{
		{"id": `"p1"`, "labels": `["c"]`, "purge": `false`, "reason": `null`},
		{"id": `"p1"`, "labels": `["a", "b"]`, "purge": `true`, "reason": `{"by": ["editor"]}`},
	})
}

// deleteProbe calls removeTags, a DELETE, with the required argument and a
// null reason, then with every argument.
const deleteProbe = `
assert sdk.tag.remove_tags("p1", ["c"], reason=None) == ["c"]
assert sdk.tag.remove_tags("p1", ["a", "b"], purge=True, reason={"by": ["editor"]}) == ["a", "b"]
`
