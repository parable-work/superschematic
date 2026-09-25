// This file runs inside the generated API module of body-args-api
// (TestBodyArgsRoutesApplyTheListRules copies it there). It registers the
// generated routes with an implementation that records what each call
// received, and drives them over httptest with the vectors the TypeScript
// server's scalar list test uses, plus required single values of every
// builtin type, a UUID list and a list of objects.
package bodyargsapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/body-args-api"
	types "example.com/schemas/types/go/body-args-api"
)

// tags records the arguments of the last call, by wire name; calls counts
// every call, so a test can check a refused request never arrived.
type tags struct {
	calls int
	last  map[string]any
}

func (s *tags) record(args map[string]any) {
	s.calls++
	s.last = args
}

func (s *tags) SaveTags(_ context.Context, id string, labels []string, weights []float64, ranks []int64, shades []types.Shade, links []types.NetworkUrl, title string, priority float64) ([]string, error) {
	s.record(map[string]any{"id": id, "labels": labels, "weights": weights, "ranks": ranks, "shades": shades, "links": links, "title": title, "priority": priority})
	return labels, nil
}

func (s *tags) SetFlags(_ context.Context, id string, pinned bool, score float64, caption string, rank int64, related []types.IdentityUUID, points []types.Point) (*bool, error) {
	s.record(map[string]any{"id": id, "pinned": pinned, "score": score, "caption": caption, "rank": rank, "related": related, "points": points})
	return &pinned, nil
}

func (s *tags) StoreDocument(_ context.Context, document types.GenericJSON, note types.GenericJSON, extras []types.GenericJSON, grid [][]types.GenericJSON) (*types.GenericJSON, error) {
	s.record(map[string]any{"document": document, "note": note, "extras": extras, "grid": grid})
	return &document, nil
}

func (s *tags) NameShades(_ context.Context, id string, shadeByName map[string]types.Shade, linksByLocale map[string][]types.NetworkUrl) (*bool, error) {
	s.record(map[string]any{"id": id, "shadeByName": shadeByName, "linksByLocale": linksByLocale})
	named := true
	return &named, nil
}

func (s *tags) PlacePoints(_ context.Context, id string, pointByName map[string]types.Point) (*bool, error) {
	s.record(map[string]any{"id": id, "pointByName": pointByName})
	placed := true
	return &placed, nil
}

func (s *tags) FindTags(_ context.Context, labels []string) ([]string, error) {
	s.record(map[string]any{"labels": labels})
	return labels, nil
}

func serve(t *testing.T) (*httptest.Server, *tags) {
	t.Helper()
	impl := &tags{}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Tag: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl
}

// send sends body (a JSON document, or "" for none) and returns the status
// and the decoded response object.
func send(t *testing.T, server *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s %s: response is not a JSON object: %s", method, path, raw)
	}
	return resp.StatusCode, decoded
}

const (
	saveTagsPath      = "/api/posts/p1/tags"
	setFlagsPath      = "/api/posts/p1/flags"
	storeDocumentPath = "/api/documents"
	nameShadesPath    = "/api/posts/p1/shade-names"
	placePointsPath   = "/api/posts/p1/points"
	findTagsPath      = "/api/posts/tags"
)

// accepted sends body and checks the implementation was called with it.
func accepted(t *testing.T, server *httptest.Server, impl *tags, method, path, body string) map[string]any {
	t.Helper()
	before := impl.calls
	status, response := send(t, server, method, path, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s: status = %d, want 200; response %v", method, path, status, response)
	}
	if impl.calls != before+1 {
		t.Fatalf("%s %s %s: the implementation was not called", method, path, body)
	}
	return response
}

// refused sends body, checks the route answered 400 without calling the
// implementation, and returns the response.
func refused(t *testing.T, server *httptest.Server, impl *tags, method, path, body string) map[string]any {
	t.Helper()
	before := impl.calls
	status, response := send(t, server, method, path, body)
	if status != http.StatusBadRequest {
		t.Fatalf("%s %s %s: status = %d, want 400; response %v", method, path, body, status, response)
	}
	if impl.calls != before {
		t.Fatalf("%s %s %s: a refused request reached the implementation", method, path, body)
	}
	return response
}

// fieldError is one expected validation error: the path it is reported at,
// the rule that names it and, when not empty, its message.
type fieldError struct{ path, validator, message string }

// refusedWith sends body and checks the 400 response carries exactly the
// expected validation errors. A nested object element's errors sit under its
// path: "points[0].x" is errors["points[0]"]["x"].
func refusedWith(t *testing.T, server *httptest.Server, impl *tags, method, path, body string, want ...fieldError) {
	t.Helper()
	response := refused(t, server, impl, method, path, body)
	errs, ok := response["errors"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s: response has no errors object: %v", path, body, response)
	}
	got := map[string]fieldError{}
	flatten("", errs, got)
	wanted := map[string]fieldError{}
	for _, w := range want {
		wanted[w.path] = w
	}
	if len(got) != len(wanted) {
		t.Fatalf("%s %s: errors = %v, want %v", path, body, errs, want)
	}
	for key, w := range wanted {
		g, ok := got[key]
		if !ok || g.validator != w.validator || (w.message != "" && g.message != w.message) {
			t.Errorf("%s %s: error at %s = %+v, want %+v; errors %v", path, body, key, g, w, errs)
		}
	}
}

// flatten collects the single error at each path of a ValidationErrors
// object, joining nested object paths with a dot.
func flatten(prefix string, errs map[string]any, out map[string]fieldError) {
	for key, value := range errs {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch v := value.(type) {
		case map[string]any:
			flatten(path, v, out)
		case []any:
			for _, entry := range v {
				e, _ := entry.(map[string]any)
				validator, _ := e["validator"].(string)
				message, _ := e["message"].(string)
				if existing, ok := out[path]; ok {
					validator = existing.validator + "+" + validator
				}
				out[path] = fieldError{path: path, validator: validator, message: message}
			}
		}
	}
}

// jsonEqual reports whether a received value re-encodes to the JSON value
// of want.
func jsonEqual(t *testing.T, got any, want string) bool {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(encoded, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(g, w)
}

// lists is one row per list argument of saveTags: a valid list, and
// elements of the wrong JSON type with the message they are refused with.
var lists = []struct {
	name, valid string
	wrong       []string
	message     string
}{
	{"labels", `["a", "b"]`, []string{`5`, `{"a": 1}`, `true`, `["a"]`}, "expected a string"},
	{"weights", `[1.5, -2, 0]`, []string{`"5"`, `true`, `[1]`}, "expected a number"},
	{"ranks", `[1, 2]`, []string{`"2"`, `1.5`, `false`}, "expected an integer"},
	{"shades", `["light", "dark"]`, []string{`1`, `{"shade": "light"}`}, "expected a string"},
	{"links", `["https://a.test"]`, []string{`42`, `{}`}, "expected a string"},
}

// saveTagsBody is a saveTags body with name set to list and labels, the
// required list, empty unless name is labels.
func saveTagsBody(name, list string) string {
	if name == "labels" {
		return `{"labels": ` + list + `}`
	}
	return `{"labels": [], "` + name + `": ` + list + `}`
}

// firstElement is the first element of a list row's valid JSON array.
func firstElement(t *testing.T, list string) string {
	t.Helper()
	var elements []json.RawMessage
	if err := json.Unmarshal([]byte(list), &elements); err != nil {
		t.Fatal(err)
	}
	return string(elements[0])
}

func TestValidListsReachTheImplementationAsTheirJSONValues(t *testing.T) {
	server, impl := serve(t)
	for _, list := range lists {
		accepted(t, server, impl, http.MethodPut, saveTagsPath, saveTagsBody(list.name, list.valid))
		if !jsonEqual(t, impl.last[list.name], list.valid) {
			t.Errorf("%s: the implementation received %v, want %s", list.name, impl.last[list.name], list.valid)
		}
	}
}

func TestAListElementIsOneJSONValue(t *testing.T) {
	server, impl := serve(t)
	response := accepted(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": ["a,b", "", " c "], "title": "x, y"}`)
	if want := []string{"a,b", "", " c "}; !reflect.DeepEqual(impl.last["labels"], want) {
		t.Errorf("labels = %#v, want %#v", impl.last["labels"], want)
	}
	if impl.last["title"] != "x, y" {
		t.Errorf("title = %#v", impl.last["title"])
	}
	if !jsonEqual(t, response["data"], `["a,b", "", " c "]`) {
		t.Errorf("data = %v", response["data"])
	}
}

func TestANullElementIsRequiredAtItsIndex(t *testing.T) {
	server, impl := serve(t)
	for _, list := range lists {
		body := saveTagsBody(list.name, `[`+firstElement(t, list.valid)+`, null]`)
		refusedWith(t, server, impl, http.MethodPut, saveTagsPath, body, fieldError{list.name + "[1]", "required", "required field"})
	}
}

func TestAnElementOfTheWrongJSONTypeIsATypeError(t *testing.T) {
	server, impl := serve(t)
	for _, list := range lists {
		for _, element := range list.wrong {
			body := saveTagsBody(list.name, `[`+firstElement(t, list.valid)+`, `+element+`]`)
			refusedWith(t, server, impl, http.MethodPut, saveTagsPath, body, fieldError{list.name + "[1]", "type", list.message})
		}
	}
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "priority": "5"}`, fieldError{"priority", "type", "expected a number"})
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "title": 5}`, fieldError{"title", "type", "expected a string"})
}

func TestRequiredMeansPresent(t *testing.T) {
	server, impl := serve(t)
	accepted(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": []}`)
	if labels, ok := impl.last["labels"].([]string); !ok || labels == nil || len(labels) != 0 {
		t.Errorf("labels = %#v, want an empty, non-nil list", impl.last["labels"])
	}
	for _, body := range []string{`{}`, `{"labels": null}`} {
		refusedWith(t, server, impl, http.MethodPut, saveTagsPath, body, fieldError{"labels", "required", "required field"})
	}
	accepted(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "weights": null}`)
	if weights := impl.last["weights"].([]float64); weights != nil {
		t.Errorf("an absent optional list reached the implementation as %#v, want nil", weights)
	}
	// Keys match exactly, as in the TypeScript server: "Labels" is not labels.
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"Labels": ["a"]}`, fieldError{"labels", "required", "required field"})
}

func TestListBoundsApplyToTheList(t *testing.T) {
	server, impl := serve(t)
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": ["a", "b", "c", "d"]}`, fieldError{"labels", "listMax", "must contain at most 3 items"})
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "links": []}`, fieldError{"links", "listMin", "must contain at least 1 items"})
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "links": ["https://a.test", "https://b.test", "https://c.test"]}`, fieldError{"links", "listMax", "must contain at most 2 items"})
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": "a,b"}`, fieldError{"labels", "type", "expected an array"})
}

func TestTheScalarsRulesApplyToEachElementNamedByTheRule(t *testing.T) {
	server, impl := serve(t)
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "links": ["https://a.test", "not a url"]}`, fieldError{"links[1]", "pattern", ""})
	long := `"https://` + strings.Repeat("a", 2050) + `.test"`
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "links": [`+long+`]}`, fieldError{"links[0]", "maxLength", "must be at most 2048 characters"})
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "ranks": [2, 0]}`, fieldError{"ranks[1]", "min", "must be at least 1"})
	// The argument's own pattern applies after the scalar's.
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "links": ["http://a.test"]}`, fieldError{"links[0]", "pattern", ""})
}

func TestAnEnumElementOutsideTheEnumIsRefusedAtItsIndex(t *testing.T) {
	server, impl := serve(t)
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"labels": [], "shades": ["light", "dim"]}`, fieldError{"shades[1]", "enum", ""})
}

func TestErrorsOfEveryArgumentAreReportedTogether(t *testing.T) {
	server, impl := serve(t)
	refusedWith(t, server, impl, http.MethodPut, saveTagsPath, `{"weights": [null], "shades": ["dim"], "ranks": [0, "1"]}`,
		fieldError{"labels", "required", ""},
		fieldError{"weights[0]", "required", ""},
		fieldError{"shades[0]", "enum", ""},
		fieldError{"ranks[0]", "min", ""},
		fieldError{"ranks[1]", "type", "expected an integer"},
	)
}

func TestRequiredSingleValuesOfEveryBuiltinType(t *testing.T) {
	server, impl := serve(t)
	accepted(t, server, impl, http.MethodPost, setFlagsPath, `{"pinned": false, "score": 0, "caption": "ok", "rank": 1}`)
	if impl.last["pinned"] != false || impl.last["score"] != 0.0 || impl.last["caption"] != "ok" || impl.last["rank"] != int64(1) {
		t.Errorf("the implementation received %v", impl.last)
	}
	refusedWith(t, server, impl, http.MethodPost, setFlagsPath, `{}`,
		fieldError{"pinned", "required", "required field"},
		fieldError{"score", "required", "required field"},
		fieldError{"caption", "required", "required field"},
		fieldError{"rank", "required", "required field"},
	)
	refusedWith(t, server, impl, http.MethodPost, setFlagsPath, `{"pinned": "true", "score": 11, "caption": "a", "rank": 0}`,
		fieldError{"pinned", "type", "expected a boolean"},
		fieldError{"score", "max", "must be at most 10"},
		fieldError{"caption", "minLength", "must be at least 2 characters"},
		fieldError{"rank", "min", "must be at least 1"},
	)
}

func TestAUUIDListAndAListOfObjects(t *testing.T) {
	server, impl := serve(t)
	const flags = `"pinned": true, "score": 1, "caption": "ok", "rank": 1`
	const uuid = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
	accepted(t, server, impl, http.MethodPost, setFlagsPath, `{`+flags+`, "related": ["`+uuid+`"], "points": [{"x": 1, "y": 2}]}`)
	if related := impl.last["related"].([]types.IdentityUUID); len(related) != 1 || related[0].IsZero() {
		t.Errorf("related = %v", related)
	}
	if points := impl.last["points"].([]types.Point); !reflect.DeepEqual(points, []types.Point{{X: 1, Y: 2}}) {
		t.Errorf("points = %+v", points)
	}
	refusedWith(t, server, impl, http.MethodPost, setFlagsPath, `{`+flags+`, "related": ["`+uuid+`", "not a uuid", null, 7]}`,
		fieldError{"related[1]", "pattern", "invalid format"},
		fieldError{"related[2]", "required", "required field"},
		fieldError{"related[3]", "type", "expected a string"},
	)
	refusedWith(t, server, impl, http.MethodPost, setFlagsPath, `{`+flags+`, "points": [{"x": -1, "y": 0}, null, 5, {"x": "1", "y": 0}]}`,
		fieldError{"points[0].x", "min", ""},
		fieldError{"points[1]", "required", "required field"},
		fieldError{"points[2]", "type", "expected an object"},
		fieldError{"points[3]", "type", "does not match the declared type"},
	)
}

func TestTheBodyIsOneJSONObject(t *testing.T) {
	server, impl := serve(t)
	for _, body := range []string{`null`, `[]`, `"labels"`, `{"labels": [}`} {
		response := refused(t, server, impl, http.MethodPut, saveTagsPath, body)
		if response["errors"] != nil {
			t.Errorf("%s: a body that is not an object has field errors: %v", body, response)
		}
	}
}

func TestAGenericJSONArgumentIsAnyJSONValueButNull(t *testing.T) {
	server, impl := serve(t)
	for _, document := range []string{`{"a": [1, null]}`, `[1, "a"]`, `"text"`, `""`, `0`, `false`} {
		response := accepted(t, server, impl, http.MethodPost, storeDocumentPath, `{"document": `+document+`}`)
		if !jsonEqual(t, impl.last["document"], document) || !jsonEqual(t, response["data"], document) {
			t.Errorf("document %s: the implementation received %s, the response data is %v", document, impl.last["document"], response["data"])
		}
	}
	for _, body := range []string{`{}`, `{"document": null}`} {
		refusedWith(t, server, impl, http.MethodPost, storeDocumentPath, body, fieldError{"document", "required", "required field"})
	}
	// An optional one may be left out or null; it is then absent.
	for _, body := range []string{`{"document": 1}`, `{"document": 1, "note": null}`} {
		accepted(t, server, impl, http.MethodPost, storeDocumentPath, body)
		if note := impl.last["note"].(types.GenericJSON); len(note) != 0 {
			t.Errorf("%s: note = %s, want absent", body, note)
		}
	}
}

func TestAGenericJSONListTakesAnyElementButNull(t *testing.T) {
	server, impl := serve(t)
	const extras = `[1, "a", {"b": 2}, [3, null], true]`
	const grid = `[[{"a": 1}, "x"], []]`
	accepted(t, server, impl, http.MethodPost, storeDocumentPath, `{"document": 1, "extras": `+extras+`, "grid": `+grid+`}`)
	if !jsonEqual(t, impl.last["extras"], extras) || !jsonEqual(t, impl.last["grid"], grid) {
		t.Errorf("the implementation received extras %v, grid %v", impl.last["extras"], impl.last["grid"])
	}
	refusedWith(t, server, impl, http.MethodPost, storeDocumentPath, `{"document": 1, "extras": [1, null]}`, fieldError{"extras[1]", "required", "required field"})
	refusedWith(t, server, impl, http.MethodPost, storeDocumentPath, `{"document": 1, "grid": [[null]]}`, fieldError{"grid[0][0]", "required", "required field"})
	refusedWith(t, server, impl, http.MethodPost, storeDocumentPath, `{"document": 1, "grid": [null]}`, fieldError{"grid[0]", "required", "required field"})
	refusedWith(t, server, impl, http.MethodPost, storeDocumentPath, `{"document": 1, "grid": [{}]}`, fieldError{"grid[0]", "type", "expected an array"})
}

func TestAMapArgumentIsAJSONObject(t *testing.T) {
	server, impl := serve(t)
	accepted(t, server, impl, http.MethodPut, nameShadesPath, `{"shadeByName": {"a": "light", "b": "dark"}, "linksByLocale": {"en": ["https://a.test"], "fr": []}}`)
	if want := map[string]types.Shade{"a": types.Shade_Light, "b": types.Shade_Dark}; !reflect.DeepEqual(impl.last["shadeByName"], want) {
		t.Errorf("shadeByName = %#v, want %#v", impl.last["shadeByName"], want)
	}
	if want := map[string][]types.NetworkUrl{"en": {"https://a.test"}, "fr": {}}; !reflect.DeepEqual(impl.last["linksByLocale"], want) {
		t.Errorf("linksByLocale = %#v, want %#v", impl.last["linksByLocale"], want)
	}
	accepted(t, server, impl, http.MethodPut, nameShadesPath, `{"shadeByName": {}}`)
	if shades := impl.last["shadeByName"].(map[string]types.Shade); shades == nil || len(shades) != 0 {
		t.Errorf("{} = %#v, want an empty, non-nil map", shades)
	}
	if links := impl.last["linksByLocale"].(map[string][]types.NetworkUrl); links != nil {
		t.Errorf("an absent optional map = %#v, want nil", links)
	}
	for _, body := range []string{`{}`, `{"shadeByName": null}`} {
		refusedWith(t, server, impl, http.MethodPut, nameShadesPath, body, fieldError{"shadeByName", "required", "required field"})
	}
	for _, body := range []string{`{"shadeByName": "light"}`, `{"shadeByName": ["light"]}`} {
		refusedWith(t, server, impl, http.MethodPut, nameShadesPath, body, fieldError{"shadeByName", "type", "expected an object"})
	}
}

func TestEachMapValueIsCheckedAtItsKey(t *testing.T) {
	server, impl := serve(t)
	refusedWith(t, server, impl, http.MethodPut, nameShadesPath, `{"shadeByName": {"a": "dim", "b": null, "c": 5}}`,
		fieldError{"shadeByName[a]", "enum", ""},
		fieldError{"shadeByName[b]", "required", "required field"},
		fieldError{"shadeByName[c]", "type", "expected a string"},
	)
	refusedWith(t, server, impl, http.MethodPut, nameShadesPath, `{"shadeByName": {}, "linksByLocale": {"en": ["http://a.test", null], "fr": "https://a.test", "de": null}}`,
		fieldError{"linksByLocale[en][0]", "pattern", "invalid format"},
		fieldError{"linksByLocale[en][1]", "required", "required field"},
		fieldError{"linksByLocale[fr]", "type", "expected an array"},
		fieldError{"linksByLocale[de]", "required", "required field"},
	)
}

func TestAMapOfAnObjectTypeIsABodyArgument(t *testing.T) {
	server, impl := serve(t)
	accepted(t, server, impl, http.MethodPut, placePointsPath, `{"pointByName": {"a": {"x": 1, "y": 2}}}`)
	if want := map[string]types.Point{"a": {X: 1, Y: 2}}; !reflect.DeepEqual(impl.last["pointByName"], want) {
		t.Errorf("pointByName = %#v, want %#v", impl.last["pointByName"], want)
	}
	refusedWith(t, server, impl, http.MethodPut, placePointsPath, `{"pointByName": {"a": {"x": -1, "y": 0}, "b": null}}`,
		fieldError{"pointByName[a].x", "min", ""},
		fieldError{"pointByName[b]", "required", "required field"},
	)
	// The body is not a Point: the map is the argument named pointByName.
	refusedWith(t, server, impl, http.MethodPut, placePointsPath, `{"x": 1, "y": 2}`, fieldError{"pointByName", "required", "required field"})
}

func TestAGETListReadsEveryQueryKey(t *testing.T) {
	server, impl := serve(t)
	accepted(t, server, impl, http.MethodGet, findTagsPath+"?labels=a,b&labels=c", "")
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(impl.last["labels"], want) {
		t.Errorf("labels = %#v, want %#v", impl.last["labels"], want)
	}
	refused(t, server, impl, http.MethodGet, findTagsPath, "")
}
