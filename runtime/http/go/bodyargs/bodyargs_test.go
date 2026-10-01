package bodyargs

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// shade is an enum as the generated types write one: a string with a
// Validate that reports "enum".
type shade string

func (s shade) Validate() (bool, []validate.ValidationError) {
	if s == "light" || s == "dark" {
		return true, nil
	}
	return false, []validate.ValidationError{{Validator: "enum", Message: "invalid enum value"}}
}

// point is an object type as the generated types write one: Validate has a
// pointer receiver and nests field errors.
type point struct {
	X float64 `json:"x"`
}

func (p *point) Validate() validate.ValidationErrors {
	errs := validate.NewValidationErrors()
	if p.X < 0 {
		errs.AddFieldError("x", "min", "must be at least 0")
	}
	return errs
}

// errorsOf decodes a request body, runs decode and returns the recorded
// errors as path -> "validator: message".
func errorsOf(t *testing.T, body string, decode func(validate.ValidationErrors, Body)) map[string]string {
	t.Helper()
	parsed, err := ReadObject(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ReadObject(%s): %v", body, err)
	}
	errs := validate.NewValidationErrors()
	decode(errs, parsed)
	return flatten("", errs)
}

func flatten(prefix string, errs validate.ValidationErrors) map[string]string {
	out := map[string]string{}
	for key, value := range errs {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch v := value.(type) {
		case []validate.ValidationError:
			var parts []string
			for _, e := range v {
				parts = append(parts, e.Validator+": "+e.Message)
			}
			out[path] = strings.Join(parts, "; ")
		case validate.ValidationErrors:
			for nestedPath, message := range flatten(path, v) {
				out[nestedPath] = message
			}
		}
	}
	return out
}

func TestReadObject(t *testing.T) {
	body, err := ReadObject(strings.NewReader(` {"a": [1, null], "b": "x"} `))
	if err != nil {
		t.Fatal(err)
	}
	if string(body["a"]) != `[1, null]` || string(body["b"]) != `"x"` {
		t.Errorf("body = %v", body)
	}
	for _, input := range []string{`null`, `[]`, `"a"`, `5`, `true`} {
		if _, err := ReadObject(strings.NewReader(input)); !errors.Is(err, ErrNotObject) {
			t.Errorf("ReadObject(%s) = %v, want ErrNotObject", input, err)
		}
	}
	for _, input := range []string{``, `{`, `{"a": }`} {
		if _, err := ReadObject(strings.NewReader(input)); err == nil || errors.Is(err, ErrNotObject) {
			t.Errorf("ReadObject(%q) = %v, want a decode error", input, err)
		}
	}
}

func TestValueRequiredMeansPresent(t *testing.T) {
	required := NewArg("title", String, Required())
	for _, body := range []string{`{}`, `{"title": null}`} {
		got := errorsOf(t, body, func(errs validate.ValidationErrors, b Body) { Value[string](errs, b, required) })
		if want := map[string]string{"title": "required: required field"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: errors = %v, want %v", body, got, want)
		}
	}
	optional := NewArg("title", String)
	got := errorsOf(t, `{"title": null}`, func(errs validate.ValidationErrors, b Body) {
		if v := Value[string](errs, b, optional); v != "" {
			t.Errorf("an absent optional value = %q", v)
		}
	})
	if len(got) != 0 {
		t.Errorf("an absent optional value has errors %v", got)
	}
	// A present zero value satisfies a required argument.
	for _, body := range []string{`{"title": ""}`, `{"n": 0}`, `{"b": false}`} {
		got := errorsOf(t, body, func(errs validate.ValidationErrors, b Body) {
			Value[string](errs, b, NewArg("title", String))
			Value[float64](errs, b, NewArg("n", Number))
			Value[bool](errs, b, NewArg("b", Boolean))
		})
		if len(got) != 0 {
			t.Errorf("%s: errors = %v", body, got)
		}
	}
}

func TestKindIsTheJSONType(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
		run  func(validate.ValidationErrors, Body)
	}{
		{`{"v": 5}`, "type: expected a string", func(e validate.ValidationErrors, b Body) { Value[string](e, b, NewArg("v", String)) }},
		{`{"v": "5"}`, "type: expected a number", func(e validate.ValidationErrors, b Body) { Value[float64](e, b, NewArg("v", Number)) }},
		{`{"v": 1.5}`, "type: expected an integer", func(e validate.ValidationErrors, b Body) { Value[int64](e, b, NewArg("v", Integer)) }},
		{`{"v": "true"}`, "type: expected a boolean", func(e validate.ValidationErrors, b Body) { Value[bool](e, b, NewArg("v", Boolean)) }},
		{`{"v": []}`, "type: expected an object", func(e validate.ValidationErrors, b Body) { Value[point](e, b, NewArg("v", Object)) }},
		{`{"v": {"x": "a"}}`, "type: does not match the declared type", func(e validate.ValidationErrors, b Body) { Value[point](e, b, NewArg("v", Object)) }},
		{`{"v": {}}`, "type: expected an array", func(e validate.ValidationErrors, b Body) { Value[[]float32](e, b, NewArg("v", Array)) }},
	} {
		got := errorsOf(t, tc.body, tc.run)
		if want := map[string]string{"v": tc.want}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: errors = %v, want %v", tc.body, got, want)
		}
	}
}

func TestAnyTakesEveryJSONValueButNull(t *testing.T) {
	arg := NewArg("doc", Any, Required())
	for _, value := range []string{`{"a": [1, null]}`, `[1, "a"]`, `"text"`, `""`, `0`, `false`} {
		got := errorsOf(t, `{"doc": `+value+`}`, func(errs validate.ValidationErrors, b Body) {
			if doc := Value[json.RawMessage](errs, b, arg); string(doc) != value {
				t.Errorf("doc = %s, want %s", doc, value)
			}
		})
		if len(got) != 0 {
			t.Errorf("%s: errors = %v", value, got)
		}
	}
	got := errorsOf(t, `{"doc": null}`, func(errs validate.ValidationErrors, b Body) { Value[json.RawMessage](errs, b, arg) })
	if want := map[string]string{"doc": "required: required field"}; !reflect.DeepEqual(got, want) {
		t.Errorf("errors = %v, want %v", got, want)
	}

	// A list element is never null, a JSON value included, in a required
	// and an optional list alike; a null inside an element is part of it.
	docs := NewArg("docs", Any, Required())
	notes := NewArg("notes", Any)
	grid := NewArg("grid", Any)
	got = errorsOf(t, `{"docs": [{"a": null}, null], "notes": [null], "grid": [[1], [2, null]]}`, func(errs validate.ValidationErrors, b Body) {
		List[json.RawMessage](errs, b, docs)
		List[json.RawMessage](errs, b, notes)
		ListOfLists[json.RawMessage](errs, b, grid)
	})
	want := map[string]string{
		"docs[1]":    "required: required field",
		"notes[0]":   "required: required field",
		"grid[1][1]": "required: required field",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("null list elements: errors = %v, want %v", got, want)
	}
}

// An optional JSON value built with KeepNull takes null as a value: a
// present null reaches the caller as the JSON null token, and an absent
// one as nil. A required one still refuses null.
func TestKeepNullKeepsAPresentNullApartFromAbsent(t *testing.T) {
	note := NewArg("note", Any, KeepNull())
	for _, tc := range []struct {
		body string
		want json.RawMessage
	}{
		{`{}`, nil},
		{`{"note": null}`, json.RawMessage(`null`)},
		{`{"note": {"a": null}}`, json.RawMessage(`{"a": null}`)},
		{`{"note": false}`, json.RawMessage(`false`)},
	} {
		got := errorsOf(t, tc.body, func(errs validate.ValidationErrors, b Body) {
			value := Value[json.RawMessage](errs, b, note)
			if (value == nil) != (tc.want == nil) || string(value) != string(tc.want) {
				t.Errorf("%s: note = %#v, want %#v", tc.body, value, tc.want)
			}
		})
		if len(got) != 0 {
			t.Errorf("%s: errors = %v", tc.body, got)
		}
	}

	// Without KeepNull a present null is the zero value, as for every other
	// optional argument.
	got := errorsOf(t, `{"note": null}`, func(errs validate.ValidationErrors, b Body) {
		if value := Value[json.RawMessage](errs, b, NewArg("note", Any)); value != nil {
			t.Errorf("note without KeepNull = %s, want nil", value)
		}
	})
	if len(got) != 0 {
		t.Errorf("errors = %v", got)
	}

	got = errorsOf(t, `{"doc": null}`, func(errs validate.ValidationErrors, b Body) {
		Value[json.RawMessage](errs, b, NewArg("doc", Any, Required(), KeepNull()))
	})
	if want := map[string]string{"doc": "required: required field"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a required null: errors = %v, want %v", got, want)
	}
}

func TestListRules(t *testing.T) {
	labels := NewArg("labels", String, Required(), ListMin(1), ListMax(2))
	run := func(errs validate.ValidationErrors, b Body) { List[string](errs, b, labels) }
	for _, tc := range []struct {
		body string
		want map[string]string
	}{
		{`{"labels": ["a"]}`, map[string]string{}},
		{`{}`, map[string]string{"labels": "required: required field"}},
		{`{"labels": null}`, map[string]string{"labels": "required: required field"}},
		{`{"labels": "a,b"}`, map[string]string{"labels": "type: expected an array"}},
		{`{"labels": []}`, map[string]string{"labels": "listMin: must contain at least 1 items"}},
		{`{"labels": ["a", "b", "c"]}`, map[string]string{"labels": "listMax: must contain at most 2 items"}},
		{`{"labels": ["a", null]}`, map[string]string{"labels[1]": "required: required field"}},
		{`{"labels": [5, true]}`, map[string]string{"labels[0]": "type: expected a string", "labels[1]": "type: expected a string"}},
	} {
		if got := errorsOf(t, tc.body, run); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: errors = %v, want %v", tc.body, got, tc.want)
		}
	}

	errorsOf(t, `{"labels": []}`, func(errs validate.ValidationErrors, b Body) {
		if got := List[string](errs, b, NewArg("labels", String, Required())); got == nil || len(got) != 0 {
			t.Errorf("[] = %#v, want an empty, non-nil list", got)
		}
		if got := List[string](errs, b, NewArg("absent", String)); got != nil {
			t.Errorf("an absent optional list = %#v, want nil", got)
		}
	})
	errorsOf(t, `{"labels": ["a,b", "", " c "]}`, func(errs validate.ValidationErrors, b Body) {
		if got, want := List[string](errs, b, labels), []string{"a,b", "", " c "}; !reflect.DeepEqual(got, want) {
			t.Errorf("labels = %#v, want %#v", got, want)
		}
	})
}

func TestListOfListsRules(t *testing.T) {
	grid := NewArg("grid", Number, Required(), ListMax(2), Min(0))
	run := func(errs validate.ValidationErrors, b Body) { ListOfLists[float64](errs, b, grid) }
	for _, tc := range []struct {
		body string
		want map[string]string
	}{
		{`{"grid": [[1, 2], []]}`, map[string]string{}},
		{`{"grid": []}`, map[string]string{}},
		{`{}`, map[string]string{"grid": "required: required field"}},
		{`{"grid": [[], [], []]}`, map[string]string{"grid": "listMax: must contain at most 2 items"}},
		{`{"grid": [null]}`, map[string]string{"grid[0]": "required: required field"}},
		{`{"grid": [{}]}`, map[string]string{"grid[0]": "type: expected an array"}},
		{`{"grid": [[1], [null, -1, "2"]]}`, map[string]string{
			"grid[1][0]": "required: required field",
			"grid[1][1]": "min: must be at least 0",
			"grid[1][2]": "type: expected a number",
		}},
	} {
		if got := errorsOf(t, tc.body, run); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: errors = %v, want %v", tc.body, got, tc.want)
		}
	}
	errorsOf(t, `{"grid": [[1.5], []]}`, func(errs validate.ValidationErrors, b Body) {
		if got, want := ListOfLists[float64](errs, b, grid), [][]float64{{1.5}, {}}; !reflect.DeepEqual(got, want) {
			t.Errorf("grid = %#v, want %#v", got, want)
		}
	})
}

func TestMapRules(t *testing.T) {
	shades := NewArg("shades", String, Required())
	links := NewArg("links", String, Pattern(`^https://`))
	points := NewArg("points", Object)
	run := func(errs validate.ValidationErrors, b Body) {
		Map[shade](errs, b, shades)
		MapOfLists[string](errs, b, links)
		Map[point](errs, b, points)
	}
	for _, tc := range []struct {
		body string
		want map[string]string
	}{
		{`{"shades": {"a": "light"}, "links": {"en": ["https://a.test"], "fr": []}, "points": {"p": {"x": 1}}}`, map[string]string{}},
		{`{"shades": {}}`, map[string]string{}},
		{`{}`, map[string]string{"shades": "required: required field"}},
		{`{"shades": null}`, map[string]string{"shades": "required: required field"}},
		{`{"shades": ["light"], "links": "x"}`, map[string]string{"shades": "type: expected an object", "links": "type: expected an object"}},
		{`{"shades": {"a": "dim", "b": null, "c": 5}}`, map[string]string{
			"shades[a]": "enum: invalid enum value",
			"shades[b]": "required: required field",
			"shades[c]": "type: expected a string",
		}},
		{`{"shades": {}, "links": {"en": ["http://a.test", null], "fr": {}, "de": null}}`, map[string]string{
			"links[en][0]": "pattern: invalid format",
			"links[en][1]": "required: required field",
			"links[fr]":    "type: expected an array",
			"links[de]":    "required: required field",
		}},
		{`{"shades": {}, "points": {"p": {"x": -1}, "q": null}}`, map[string]string{
			"points[p].x": "min: must be at least 0",
			"points[q]":   "required: required field",
		}},
	} {
		if got := errorsOf(t, tc.body, run); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: errors = %v, want %v", tc.body, got, tc.want)
		}
	}
	errorsOf(t, `{"shades": {}, "links": {"en": ["https://a.test"], "fr": []}}`, func(errs validate.ValidationErrors, b Body) {
		if got := Map[shade](errs, b, shades); got == nil || len(got) != 0 {
			t.Errorf("{} = %#v, want an empty, non-nil map", got)
		}
		if got, want := MapOfLists[string](errs, b, links), map[string][]string{"en": {"https://a.test"}, "fr": {}}; !reflect.DeepEqual(got, want) {
			t.Errorf("links = %#v, want %#v", got, want)
		}
		if got := Map[point](errs, b, points); got != nil {
			t.Errorf("an absent optional map = %#v, want nil", got)
		}
	})
}

// queryErrorsOf parses a query string, runs decode and returns the recorded
// errors as path -> "validator: message".
func queryErrorsOf(t *testing.T, query string, decode func(validate.ValidationErrors, url.Values)) map[string]string {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("ParseQuery(%s): %v", query, err)
	}
	errs := validate.NewValidationErrors()
	decode(errs, values)
	return flatten("", errs)
}

// TestQueryListReadsEveryKeyAndEveryComma: the items of a GET list are every
// occurrence of its key, each split on commas, trimmed, with empty items
// dropped, and each is read as its kind's JSON value.
func TestQueryListReadsEveryKeyAndEveryComma(t *testing.T) {
	queryErrorsOf(t, "labels=a,b&labels=%20c%20,,&scores=1.5,-2,1e3&ranks=1,2&flags=true,false,1,F&at=2026-01-02T03:04:05Z", func(errs validate.ValidationErrors, q url.Values) {
		if got, want := QueryList[string](errs, q, NewArg("labels", String)), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
			t.Errorf("labels = %#v, want %#v", got, want)
		}
		if got, want := QueryList[float64](errs, q, NewArg("scores", Number)), []float64{1.5, -2, 1000}; !reflect.DeepEqual(got, want) {
			t.Errorf("scores = %#v, want %#v", got, want)
		}
		if got, want := QueryList[int64](errs, q, NewArg("ranks", Integer)), []int64{1, 2}; !reflect.DeepEqual(got, want) {
			t.Errorf("ranks = %#v, want %#v", got, want)
		}
		if got, want := QueryList[bool](errs, q, NewArg("flags", Boolean)), []bool{true, false, true, false}; !reflect.DeepEqual(got, want) {
			t.Errorf("flags = %#v, want %#v", got, want)
		}
		if got := QueryList[timestamp](errs, q, NewArg("at", String)); len(got) != 1 || got[0].value != "2026-01-02T03:04:05Z" {
			t.Errorf("at = %#v", got)
		}
		if got := QueryList[string](errs, q, NewArg("absent", String)); got != nil {
			t.Errorf("an absent optional list = %#v, want nil", got)
		}
		if len(errs) != 0 {
			t.Errorf("errors = %v", flatten("", errs))
		}
	})
}

// TestQueryListRules: no item is absent, the list bounds count the items,
// an item that is not its kind's JSON value is "type" at name[i], and each
// item passes the value rules and its type's own validation at name[i].
func TestQueryListRules(t *testing.T) {
	run := func(errs validate.ValidationErrors, q url.Values) {
		QueryList[string](errs, q, NewArg("labels", String, Required(), ListMin(2), ListMax(3), MinLength(2), Pattern(`^[a-z]+$`)))
		QueryList[float64](errs, q, NewArg("scores", Number, Min(0), Max(10)))
		QueryList[int64](errs, q, NewArg("ranks", Integer, Min(1)))
		QueryList[bool](errs, q, NewArg("flags", Boolean))
		QueryList[shade](errs, q, NewArg("shades", String))
		QueryList[timestamp](errs, q, NewArg("at", String))
	}
	for _, tc := range []struct {
		query string
		want  map[string]string
	}{
		{"labels=ab,cd", map[string]string{}},
		{"", map[string]string{"labels": "required: required field"}},
		{"labels=", map[string]string{"labels": "required: required field"}},
		{"labels=,%20,", map[string]string{"labels": "required: required field"}},
		{"labels=ab", map[string]string{"labels": "listMin: must contain at least 2 items"}},
		{"labels=ab,cd,ef,gh", map[string]string{"labels": "listMax: must contain at most 3 items"}},
		{"labels=ab,c,D1", map[string]string{"labels[1]": "minLength: must be at least 2 characters", "labels[2]": "pattern: invalid format"}},
		{"labels=ab,cd&scores=1,x,NaN,Infinity,0x10,-1,11", map[string]string{
			"scores[1]": "type: expected a number",
			"scores[2]": "type: expected a number",
			"scores[3]": "type: expected a number",
			"scores[4]": "type: expected a number",
			"scores[5]": "min: must be at least 0",
			"scores[6]": "max: must be at most 10",
		}},
		{"labels=ab,cd&ranks=1,1.5,two,0", map[string]string{
			"ranks[1]": "type: expected an integer",
			"ranks[2]": "type: expected an integer",
			"ranks[3]": "min: must be at least 1",
		}},
		{"labels=ab,cd&flags=true,yes", map[string]string{"flags[1]": "type: expected a boolean"}},
		{"labels=ab,cd&shades=light,dim", map[string]string{"shades[1]": "enum: invalid enum value"}},
		{"labels=ab,cd&at=2026-01-02T03:04:05Z,yesterday", map[string]string{"at[1]": "pattern: invalid format"}},
	} {
		if got := queryErrorsOf(t, tc.query, run); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: errors = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// TestValueRulesInOrderOneErrorEach: the rules apply in the order given
// (the scalar's, then the argument's), and the first a value breaks is its
// one error.
func TestValueRulesInOrderOneErrorEach(t *testing.T) {
	links := NewArg("links", String, MaxLength(14), Pattern(`^https?://\w+\.test$`), Pattern(`^https://`))
	ranks := NewArg("ranks", Integer, Min(1), Max(9007199254740991), Max(100))
	caption := NewArg("caption", String, MinLength(2))
	for _, tc := range []struct {
		body string
		want map[string]string
	}{
		{`{"links": ["https://a.test"], "ranks": [1, 100], "caption": "ok"}`, map[string]string{}},
		{`{"links": ["not a url"]}`, map[string]string{"links[0]": "pattern: invalid format"}},
		{`{"links": ["http://a.test"]}`, map[string]string{"links[0]": "pattern: invalid format"}},
		// Too long and malformed: maxLength comes first and is the one error.
		{`{"links": ["not a url at all"]}`, map[string]string{"links[0]": "maxLength: must be at most 14 characters"}},
		{`{"ranks": [0, 101]}`, map[string]string{"ranks[0]": "min: must be at least 1", "ranks[1]": "max: must be at most 100"}},
		{`{"caption": "a"}`, map[string]string{"caption": "minLength: must be at least 2 characters"}},
	} {
		got := errorsOf(t, tc.body, func(errs validate.ValidationErrors, b Body) {
			List[string](errs, b, links)
			List[int64](errs, b, ranks)
			Value[string](errs, b, caption)
		})
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: errors = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// TestLengthsCountCodePoints: an astral character (four UTF-8 bytes) and a
// multi-byte BMP character (two) are one each, as every validator counts.
func TestLengthsCountCodePoints(t *testing.T) {
	name := NewArg("name", String, MinLength(2), MaxLength(5))
	for _, tc := range []struct {
		body string
		want map[string]string
	}{
		{`{"name": "\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00"}`, map[string]string{}},
		{`{"name": "\u00e9\u00e9\u00e9\u00e9\u00e9"}`, map[string]string{}},
		{`{"name": "\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00\ud83d\ude00"}`, map[string]string{"name": "maxLength: must be at most 5 characters"}},
		{`{"name": "\ud83d\ude00"}`, map[string]string{"name": "minLength: must be at least 2 characters"}},
	} {
		got := errorsOf(t, tc.body, func(errs validate.ValidationErrors, b Body) { Value[string](errs, b, name) })
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: errors = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestAPatternGoCannotCompileAcceptsNoValue(t *testing.T) {
	arg := NewArg("v", String, Pattern(`(?=a)`))
	got := errorsOf(t, `{"v": "a"}`, func(errs validate.ValidationErrors, b Body) { Value[string](errs, b, arg) })
	if want := map[string]string{"v": "pattern: invalid format"}; !reflect.DeepEqual(got, want) {
		t.Errorf("errors = %v, want %v", got, want)
	}
}

// TestTheTypesOwnValidationRunsLast: an enum's membership, an object's
// field errors (nested under the element's path) and a decoder's refusal
// of a string ("pattern").
func TestTheTypesOwnValidationRunsLast(t *testing.T) {
	got := errorsOf(t, `{"shades": ["light", "dim", ""], "points": [{"x": 1}, {"x": -1}], "at": ["2026-01-02T03:04:05Z", "yesterday", ""]}`, func(errs validate.ValidationErrors, b Body) {
		List[shade](errs, b, NewArg("shades", String))
		List[point](errs, b, NewArg("points", Object))
		List[timestamp](errs, b, NewArg("at", String))
	})
	want := map[string]string{
		"shades[1]":   "enum: invalid enum value",
		"shades[2]":   "enum: invalid enum value",
		"points[1].x": "min: must be at least 0",
		"at[1]":       "pattern: invalid format",
		"at[2]":       "pattern: invalid format",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("errors = %v, want %v", got, want)
	}
}

// timestamp is a string scalar with a decoder of its own, like a UUID or a
// timestamp scalar. Like the timestamp scalar's, its decoder reads "" as
// the zero value.
type timestamp struct{ value string }

func (ts *timestamp) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s != "" && !strings.HasPrefix(s, "20") {
		return errors.New("not a timestamp")
	}
	ts.value = s
	return nil
}

func TestFormatBound(t *testing.T) {
	for v, want := range map[float64]string{1: "1", 0.5: "0.5", -2: "-2", 9007199254740991: "9007199254740991", 1e300: "1e+300"} {
		if got := formatBound(v); got != want {
			t.Errorf("formatBound(%v) = %q, want %q", v, got, want)
		}
	}
}
