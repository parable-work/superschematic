package bodyargs

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// stage is an enum as the generated types write one since D31's amendment:
// its decoder refuses a string it lacks.
type stage string

func (s stage) IsValid() bool { return s == "draft" || s == "done" }

func (stage) Values() []stage { return []stage{"draft", "done"} }

func (s stage) Validate() (bool, []validate.ValidationError) {
	if s.IsValid() {
		return true, nil
	}
	return false, []validate.ValidationError{enumError}
}

func (s *stage) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	if !stage(text).IsValid() {
		return &json.UnmarshalTypeError{Value: "string " + strconv.Quote(text), Type: reflect.TypeFor[stage]()}
	}
	*s = stage(text)
	return nil
}

// task is an object type as the generated types write one: it lists its
// JSON keys and nests its fields' errors, an InputField among them.
type task struct {
	Name   string               `json:"name"`
	Stage  stage                `json:"stage"`
	Steps  []stage              `json:"steps,omitempty"`
	Parts  []task               `json:"parts,omitempty"`
	ByName map[string]stage     `json:"byName,omitempty"`
	Next   InputField[*stage]   `json:"next,omitzero"`
	Owner  *taskOwner           `json:"owner,omitempty"`
	Extra  map[string]taskOwner `json:"extra,omitempty"`
}

type taskOwner struct {
	Stage stage `json:"stage"`
}

// InputField is the generated tri-state wrapper of an optional input field.
type InputField[T any] struct {
	Set   bool `json:"-"`
	Null  bool `json:"-"`
	Value T    `json:"-"`
}

func (f *InputField[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	if string(data) == "null" {
		f.Null = true
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

func (f InputField[T]) IsZero() bool { return !f.Set }

func (*task) JSONFieldNames() []string {
	return []string{"name", "stage", "steps", "parts", "byName", "next", "owner", "extra"}
}

func (*taskOwner) JSONFieldNames() []string { return []string{"stage"} }

func (t *task) Validate() validate.ValidationErrors {
	errs := validate.NewValidationErrors()
	if len(t.Name) > 3 {
		errs.AddFieldError("name", "maxLength", "must be at most 3 characters")
	}
	for i := range t.Parts {
		if nested := t.Parts[i].Validate(); nested.HasErrors() {
			errs.AddNestedError("parts["+strconv.Itoa(i)+"]", nested)
		}
	}
	return errs
}

func (*taskOwner) Validate() validate.ValidationErrors { return validate.NewValidationErrors() }

// TestAnEnumStringArgumentItsDecoderRefusesIsEnum: a string the enum lacks
// is "enum" at its path, alone and in a list, where any other string its
// type's decoder refuses is "pattern".
func TestAnEnumStringArgumentItsDecoderRefusesIsEnum(t *testing.T) {
	arg := NewArg("stage", String)
	list := NewArg("stages", String)
	got := errorsOf(t, `{"stage": "purple", "stages": ["done", "nope"]}`, func(errs validate.ValidationErrors, body Body) {
		Value[stage](errs, body, arg)
		List[stage](errs, body, list)
	})
	want := map[string]string{"stage": "enum: invalid enum value", "stages[1]": "enum: invalid enum value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("errors = %v, want %v", got, want)
	}
	query := validate.NewValidationErrors()
	QueryList[stage](query, map[string][]string{"stages": {"draft,purple"}}, list)
	if got := flatten("", query); !reflect.DeepEqual(got, map[string]string{"stages[1]": "enum: invalid enum value"}) {
		t.Fatalf("query errors = %v", got)
	}
}

// TestAnObjectArgumentWithAnUnknownEnum: an object whose decoder refuses a
// string an enum lacks has "enum" at that string's path, nested at every
// level, beside the object's other errors.
func TestAnObjectArgumentWithAnUnknownEnum(t *testing.T) {
	arg := NewArg("task", Object)
	got := errorsOf(t, `{"task": {"name": "long", "stage": "nope", "parts": [{"name": "ok", "stage": "done", "steps": ["draft", "x"]}]}}`,
		func(errs validate.ValidationErrors, body Body) { Value[task](errs, body, arg) })
	want := map[string]string{
		"task.name":              "maxLength: must be at most 3 characters",
		"task.stage":             "enum: invalid enum value",
		"task.parts[0].steps[1]": "enum: invalid enum value",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("errors = %v, want %v", got, want)
	}
	if got := errorsOf(t, `{"task": {"stage": 3}}`, func(errs validate.ValidationErrors, body Body) { Value[task](errs, body, arg) }); !reflect.DeepEqual(got, map[string]string{"task": "type: does not match the declared type"}) {
		t.Fatalf("an enum of another JSON type = %v", got)
	}
}

// TestDecodeRefusal: an input body whose decoder refused only strings its
// enums lack is "validation failed", each at its path, in a list, a map, an
// InputField, a nested object and a map of objects, beside the other rules
// Validate finds; any other refusal is "does not match the declared type".
func TestDecodeRefusal(t *testing.T) {
	body := `{"name": "long", "stage": "nope", "steps": ["done", "x"], "byName": {"a": "y"}, "next": "z",
		"owner": {"stage": "q"}, "extra": {"k": {"stage": "w"}}, "parts": [{"name": "toolong", "stage": "draft"}]}`
	var input task
	if err := json.Unmarshal([]byte(body), &input); err == nil {
		t.Fatal("the test's decoder took an unknown stage")
	}
	refusal := DecodeRefusal(json.RawMessage(body), &input)
	if refusal.Detail != BodyMismatch || refusal.Reason != "validation failed" {
		t.Fatalf("refusal = %+v", refusal)
	}
	want := map[string]string{
		"name":           "maxLength: must be at most 3 characters",
		"stage":          "enum: invalid enum value",
		"steps[1]":       "enum: invalid enum value",
		"byName[a]":      "enum: invalid enum value",
		"next":           "enum: invalid enum value",
		"owner.stage":    "enum: invalid enum value",
		"extra[k].stage": "enum: invalid enum value",
		"parts[0].name":  "maxLength: must be at most 3 characters",
	}
	if got := flatten("", refusal.Errors); !reflect.DeepEqual(got, want) {
		t.Fatalf("errors = %v, want %v", got, want)
	}
	for _, other := range []string{`{"stage": 3}`, `{"stage": "nope", "name": 5}`, `{"name": "ok"`} {
		refusal := DecodeRefusal(json.RawMessage(other), &input)
		if refusal.Reason != "does not match the declared type" || refusal.Errors != nil {
			t.Errorf("%s: refusal = %+v", other, refusal)
		}
	}
	if !strings.HasPrefix(DecodeRefusal(json.RawMessage(`{}`), nil).Reason, "does not match") {
		t.Fatal("a nil target is a mismatch")
	}
}
