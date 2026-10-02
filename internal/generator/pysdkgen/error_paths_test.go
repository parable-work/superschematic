package pysdkgen

import (
	"fmt"
	"os/exec"
	"testing"
)

// TestAFailureIsKeyedByItsPathFromTheArgument calls the validation helpers
// of body-args-api's tag namespace, with no server (errorPathsProbe). A
// failure inside an object is keyed under the path of the value that holds
// it: the argument (point.x), a list element (points[1].x) or an element of
// a list of lists (polygons[0][1].x, its path named once). A value that is
// no object is keyed at that path. A JSON value is checked whole, so a
// failure inside one is keyed at its path alone.
func TestAFailureIsKeyedByItsPathFromTheArgument(t *testing.T) {
	writeBodyArgsModules(t).runPythonAlone(t, errorPathsProbe)
}

// TestARefusedListElementIsKeyedAtItsIndex runs listPathsProbe against the
// generated routes of body-args-api (writeBodyArgsModules): lists the route
// accepts, in the body and in the query string, reach the implementation as
// sent. An element the route would refuse is refused before the request at
// its index, as the route keys it (points[1], related[1], extras[1]), and a
// failure inside an object element under that index (points[0].x).
func TestARefusedListElementIsKeyedAtItsIndex(t *testing.T) {
	const uuid = `"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"`
	writeBodyArgsModules(t).runPythonSDK(t, listPathsProbe, uuid, []map[string]string{
		{"points": `[{"x": 1, "y": 2}]`},
		{"scores": `[1, 2.5]`},
	})
}

// runPythonAlone runs probe, after probeHeader, with no server: sdk is a
// client of an address nothing answers, so a probe that sends a request
// fails.
func (m bodyArgsModules) runPythonAlone(t *testing.T, probe string) {
	t.Helper()
	header := fmt.Sprintf(probeHeader, m.pyTypesDir, m.pySDKDir, m.sdk.PackageName, m.sdk.SDKClassName)
	if out, err := exec.Command(m.python, "-c", header+probe, "http://127.0.0.1:9").CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// errorPathsProbe checks the keys of each ValidationError the tag
// namespace's _validate_scalar_argument and _validate_list_of_lists_argument
// raise.
const errorPathsProbe = `
tag = sdk.tag


def keys(call):
    try:
        call()
    except sdk_package.ValidationError as err:
        return sorted(err.errors)
    raise AssertionError("no ValidationError")


# A failure inside an object is keyed under the path of the value that
# holds it; a value that is no object is keyed at that path.
for value, path, want in [
    ({"x": "far", "y": 0}, "point", ["point.x"]),
    ({"y": 0}, "point", ["point.x"]),
    ({}, "point", ["point.x", "point.y"]),
    (5, "point", ["point"]),
    ({"x": "far", "y": 0}, "points[1]", ["points[1].x"]),
]:
    got = keys(lambda: tag._validate_scalar_argument(value, "Point", path))
    assert got == want, (value, path, got)

# An element of a list of lists is keyed at field[i][j], and a failure
# inside it under that path, named once.
got = keys(lambda: tag._validate_list_of_lists_argument([[{"x": 1, "y": 2}, {"x": "far"}]], "Point", "polygons"))
assert got == ["polygons[0][1].x", "polygons[0][1].y"], got

# A JSON value is checked whole, as the route checks it: a failure inside
# one, at a key or an index of it, is keyed at its path alone.
for call, want in [
    (lambda: tag._validate_scalar_argument({"en": 1}, "GenericStringMap", "labels"), ["labels"]),
    (lambda: tag._validate_scalar_argument([1, True], "EmbeddingVector", "vector"), ["vector"]),
    (lambda: tag._validate_scalar_argument({"en": 1}, "GenericStringMap", "label_sets[0]"), ["label_sets[0]"]),
    (lambda: tag._validate_list_of_lists_argument([[[1, True]]], "EmbeddingVector", "vector_grid"), ["vector_grid[0][0]"]),
]:
    got = keys(call)
    assert got == want, got
`

// listPathsProbe takes a UUID as JSON and sends lists the route accepts
// through set_flags (in the body) and search_posts (in the query string).
// Then it checks that each element the route would refuse is refused before
// the request, with one error at its path.
const listPathsProbe = `
uuid = json.loads(sys.argv[2])
flags = {"pinned": True, "score": 1, "caption": "ok", "rank": 1}

assert sdk.tag.set_flags("p1", **flags, related=[uuid], points=[{"x": 1, "y": 2}]) is True
sdk.tag.search_posts(scores=[1, 2.5], related=[uuid])

for call, field, validator in [
    (lambda: sdk.tag.set_flags("p1", **flags, points=[{"x": 1, "y": 2}, None]), "points[1]", None),
    (lambda: sdk.tag.set_flags("p1", **flags, points=[{"x": 1, "y": 2}, 5]), "points[1]", "type"),
    (lambda: sdk.tag.set_flags("p1", **flags, points=[{"x": "far", "y": 0}]), "points[0].x", "type"),
    (lambda: sdk.tag.set_flags("p1", **flags, points=[{"x": 1}]), "points[0].y", "required"),
    (lambda: sdk.tag.set_flags("p1", **flags, related=[uuid, 7]), "related[1]", "type"),
    (lambda: sdk.tag.save_tags("p1", ["a", 7]), "labels[1]", "type"),
    (lambda: sdk.tag.save_tags("p1", [], weights=[1.5, "5"]), "weights[1]", "type"),
    (lambda: sdk.tag.store_document(1, extras=[1, None]), "extras[1]", "required"),
    (lambda: sdk.tag.store_embedding({}, label_sets=[{}, ["en"]]), "label_sets[1]", None),
    (lambda: sdk.tag.search_posts(scores=[1, "x"]), "scores[1]", "type"),
    (lambda: sdk.tag.search_posts(related=[uuid, 7]), "related[1]", "type"),
]:
    try:
        call()
    except sdk_package.ValidationError as err:
        assert list(err.errors) == [field], (field, err.errors)
        if validator is not None:
            assert [e["validator"] for e in err.errors[field]] == [validator], (field, err.errors)
    else:
        raise AssertionError(f"{field} was sent")
`
