package gosdkgen

import "testing"

// TestADELETESendsItsArgumentsInTheBody runs deleteBodyArgsServerTest
// against the generated routes of body-args-api (runAgainstBodyArgsRoutes):
// the SDK sends the arguments of RemoveTags, a DELETE, in the JSON body,
// where the route reads them, a map among them.
func TestADELETESendsItsArgumentsInTheBody(t *testing.T) {
	runAgainstBodyArgsRoutes(t, "delete_body_args_test.go", deleteBodyArgsServerTest)
}

// deleteBodyArgsServerTest runs in the generated API module of
// body-args-api (runAgainstBodyArgsRoutes).
const deleteBodyArgsServerTest = `package bodyargsapi_test

import (
	"context"
	"reflect"
	"testing"

	"example.com/schemas/sdk/go/body-args-api/namespaces"
	types "example.com/schemas/types/go/body-args-api"
)

func TestTheSDKSendsTheArgumentsOfADELETEInTheBody(t *testing.T) {
	client, impl := client(t)
	reason := "merged"
	requester := types.GenericJSON(` + "`" + `{"by":"ops"}` + "`" + `)
	shades := map[string]types.Shade{"a": types.Shade_Light, "b,c": types.Shade_Dark}
	removed, err := client.TagNamespace.RemoveTags(context.Background(), "p1", namespaces.TagRemoveTagsInput{
		Labels:       []string{"a", "b,c"},
		Reason:       &reason,
		Requester:    &requester,
		ShadeByLabel: shades,
	})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if want := []string{"a", "b,c"}; !reflect.DeepEqual(removed, want) || !reflect.DeepEqual(impl.last["labels"], want) {
		t.Errorf("RemoveTags returned %v, the implementation received labels %v; want %v", removed, impl.last["labels"], want)
	}
	if impl.last["reason"] != reason || !jsonEqual(t, impl.last["requester"], string(requester)) {
		t.Errorf("the implementation received reason %v, requester %v", impl.last["reason"], impl.last["requester"])
	}
	if !reflect.DeepEqual(impl.last["shadeByLabel"], shades) {
		t.Errorf("shadeByLabel = %#v, want %#v", impl.last["shadeByLabel"], shades)
	}
}
`
