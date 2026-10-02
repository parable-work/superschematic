package pysdkgen

import "testing"

// TestADELETESendsItsArgumentsInTheBody runs deleteBodyArgsProbe against
// the generated routes of body-args-api (writeBodyArgsModules): the Python
// SDK sends the arguments of remove_tags, a DELETE, in the JSON body,
// where the route reads them, as it sends those of a PUT. A label holding
// a comma stays one label, and a Generic.JSON argument is the JSON value
// it holds.
func TestADELETESendsItsArgumentsInTheBody(t *testing.T) {
	modules := writeBodyArgsModules(t)
	modules.runPythonSDK(t, deleteBodyArgsProbe, "", []map[string]string{
		{"id": `"p1"`, "labels": `["a", "b,c"]`, "reason": `"merged"`},
		{"id": `"p1"`, "labels": `["a"]`, "reason": `""`, "requester": `{"by": "ops"}`},
	})
}

// deleteBodyArgsProbe calls remove_tags with a list, a string and a
// Generic.JSON argument, and checks each call returns the labels sent.
const deleteBodyArgsProbe = `
removed = sdk.tag.remove_tags("p1", ["a", "b,c"], reason="merged")
assert removed == ["a", "b,c"], removed
removed = sdk.tag.remove_tags("p1", ["a"], requester={"by": "ops"})
assert removed == ["a"], removed
`
