package pysdkgen

import "testing"

// TestAnObjectArgumentIsCheckedByItsTypesRules runs objectArgsUnitProbe
// against body-args-api's Python SDK with the client's request stubbed
// (writeBodyArgsPython): an object argument, alone, in a list
// (set_flags' points) and in a list of lists (pin_points' grid), is
// checked by its type's rules (validate_all) as an input type is, every
// failure keyed by wire name under the argument's path (point.x,
// points[0].x, grid[0][1].x). None is required at its path and a value
// that is no object a type error there, as the route names them. A valid
// object is sent as validated, by wire name.
func TestAnObjectArgumentIsCheckedByItsTypesRules(t *testing.T) {
	writeBodyArgsPython(t).runPythonProbe(t, objectArgsUnitProbe)
}

// TestObjectArgumentsReachTheGoServer runs objectArgsProbe against the
// generated routes of body-args-api (writeBodyArgsModules): the
// implementation receives each object of set_flags' points and of
// pin_points' grid as the SDK validated it, the text "1" as the number 1
// and pin_label as pinLabel, and an object the route would refuse is
// refused before the request, with the route's path and rule.
func TestObjectArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	modules.runPythonSDK(t, objectArgsProbe, "", []map[string]string{
		{"points": `[{"x": 1, "y": 0, "pinLabel": "ab"}, {"x": 0, "y": 2}]`},
		{"grid": `[[{"x": 1, "y": 2}, {"x": 0, "y": 0, "pinLabel": "ab"}], []]`},
	})
}

// objectArgsUnitProbe stubs the request of sdk.tag's client, recording
// each body as the client would send it.
const objectArgsUnitProbe = `
Point = __import__(sys.argv[2]).Point
sent = []


def request(*, body, **kwargs):
    sent.append(json.loads(sdk.tag._client.to_json(body)))
    return True


sdk.tag._client.request = request


def refused(call):
    try:
        call()
    except sdk_package.ValidationError as err:
        return {key: [e["validator"] for e in entries] for key, entries in err.errors.items()}
    raise AssertionError("the call was not refused")


flags = {"id": "p1", "pinned": True, "score": 1, "caption": "ok", "rank": 1}
ok = {"x": 0, "y": 0}
uuid = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
for call, want in [
    # The type's rules, by wire name under the argument's path: alone (an
    # object type alone is a body argument when it is no input type), in a
    # list and in a list of lists.
    (lambda: sdk.tag._validate_scalar_argument({"x": -1, "y": 0}, "Point", "point"), {"point.x": ["min"]}),
    (lambda: sdk.tag.set_flags(**flags, points=[ok, {"x": -1, "y": 0, "pinLabel": "a"}]), {"points[1].x": ["min"], "points[1].pinLabel": ["minLength"]}),
    (lambda: sdk.tag.pin_points("p1", [[ok], [ok, {"x": -1, "y": 0}]]), {"grid[1][1].x": ["min"]}),
    # A model the caller built is checked the same way.
    (lambda: sdk.tag.set_flags(**flags, points=[Point(x=-1, y=0)]), {"points[0].x": ["min"]}),
    # pydantic's own failures sit under the same path.
    (lambda: sdk.tag.set_flags(**flags, points=[{"x": "far"}]), {"points[0].x": ["type"], "points[0].y": ["required"]}),
    (lambda: sdk.tag.pin_points("p1", [[{"y": 0}]]), {"grid[0][0].x": ["required"]}),
    # None is required at its path, of any type; a value that is no
    # object, or a row that is no list, is a type error there.
    (lambda: sdk.tag._validate_scalar_argument(None, "Point", "point"), {"point": ["required"]}),
    (lambda: sdk.tag.set_flags(**flags, points=[ok, None]), {"points[1]": ["required"]}),
    (lambda: sdk.tag.set_flags(**flags, points=[5]), {"points[0]": ["type"]}),
    (lambda: sdk.tag.set_flags(**flags, related=[uuid, None]), {"related[1]": ["required"]}),
    (lambda: sdk.tag.set_flags(**flags, related=[uuid, 7]), {"related[1]": ["type"]}),
    (lambda: sdk.tag.pin_points("p1", [[None]]), {"grid[0][0]": ["required"]}),
    (lambda: sdk.tag.pin_points("p1", [[ok], None]), {"grid[1]": ["required"]}),
    (lambda: sdk.tag.pin_points("p1", [ok]), {"grid[0]": ["type"]}),
    (lambda: sdk.tag.pin_points("p1", None), {"grid": ["required"]}),
    (lambda: sdk.tag.save_tags("p1", None), {"labels": ["required"]}),
    (lambda: sdk.tag.search_posts(ranks=[1, "2"]), {"ranks[1]": ["type"]}),
]:
    got = refused(call)
    assert got == want, (want, got)
assert sent == [], sent

# A valid object is sent as validated, by wire name: pydantic reads the
# text "1" as the number 1, a field may be given by its Python name, and
# one left out is not sent.
sdk.tag.set_flags(**flags, points=[{"x": "1", "y": 0, "pin_label": "ab"}, Point(x=2, y=3)])
assert sent[-1]["points"] == [{"x": 1, "y": 0, "pinLabel": "ab"}, {"x": 2, "y": 3}], sent[-1]
assert isinstance(sent[-1]["points"][0]["x"], float), sent[-1]
sdk.tag.pin_points("p1", [[{"x": "1", "y": 2}], []])
assert sent[-1] == {"grid": [[{"x": 1, "y": 2}], []]}, sent[-1]
assert isinstance(sent[-1]["grid"][0][0]["x"], float), sent[-1]
point = sdk.tag._validate_scalar_argument({"x": "1", "y": 0, "pin_label": "ab"}, "Point", "point")
assert point == {"x": 1, "y": 0, "pinLabel": "ab"}, point
`

// objectArgsProbe sends objects the route reads, then checks that objects
// the route refuses (TestAUUIDListAndAListOfObjects and
// TestAListOfListsOfAnObjectType in apigen's route test) are refused
// before the request, at the route's paths and with its rules.
const objectArgsProbe = `
flags = {"pinned": True, "score": 1, "caption": "ok", "rank": 1}
assert sdk.tag.set_flags("p1", **flags, points=[{"x": "1", "y": 0, "pin_label": "ab"}, {"x": 0, "y": 2}]) is True
assert sdk.tag.pin_points("p1", [[{"x": 1, "y": 2}, {"x": 0, "y": 0, "pinLabel": "ab"}], []]) is True

for call, want in [
    (lambda: sdk.tag.set_flags("p1", **flags, points=[{"x": -1, "y": 0}]), {"points[0].x": ["min"]}),
    (lambda: sdk.tag.set_flags("p1", **flags, points=[{"x": 0, "y": 0}, None]), {"points[1]": ["required"]}),
    (lambda: sdk.tag.set_flags("p1", **flags, points=[5]), {"points[0]": ["type"]}),
    (lambda: sdk.tag.pin_points("p1", [[{"x": 0, "y": 0}, {"x": -1, "y": 0, "pinLabel": "a"}]]), {
        "grid[0][1].x": ["min"],
        "grid[0][1].pinLabel": ["minLength"],
    }),
    (lambda: sdk.tag.pin_points("p1", [[None]]), {"grid[0][0]": ["required"]}),
    (lambda: sdk.tag.pin_points("p1", [[5]]), {"grid[0][0]": ["type"]}),
    (lambda: sdk.tag.pin_points("p1", [{}]), {"grid[0]": ["type"]}),
]:
    try:
        call()
    except sdk_package.ValidationError as err:
        got = {key: [e["validator"] for e in entries] for key, entries in err.errors.items()}
        assert got == want, (want, got)
    else:
        raise AssertionError(f"{want} was sent")
`
