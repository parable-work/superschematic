package bodyargs

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// failingRead fails every read with err.
type failingRead struct{ err error }

func (f failingRead) Read([]byte) (int, error) { return 0, f.err }

// TestReadOptions: each option changes the one check it names, and the
// checks it does not name stay as ReadInput makes them.
func TestReadOptions(t *testing.T) {
	fields := []string{"name"}
	for _, tc := range []struct {
		name   string
		body   string
		opts   []ReadOption
		raw    string
		detail string
	}{
		{name: "an undeclared key kept", body: `{"name":"a","Name":"b","extra":1}`, opts: []ReadOption{KeepUndeclaredKeys()}, raw: `{"name":"a","Name":"b","extra":1}`},
		{name: "an undeclared key refused", body: `{"name":"a","extra":1}`, detail: BodyMismatch},
		{name: "kept keys, not an object", body: `[1]`, opts: []ReadOption{KeepUndeclaredKeys()}, detail: BodyMismatch},
		{name: "trailing data read past", body: `{"name":"a"} trailing`, opts: []ReadOption{FirstValue()}, raw: `{"name":"a"}`},
		{name: "a second value read past", body: `{"name":"a"}{}`, opts: []ReadOption{FirstValue()}, raw: `{"name":"a"}`},
		{name: "trailing data refused", body: `{"name":"a"} trailing`, detail: BodyNotJSON},
		{name: "first value, an empty body", body: " \n", opts: []ReadOption{FirstValue()}, detail: BodyRequired},
		{name: "first value, not JSON", body: `{"name":`, opts: []ReadOption{FirstValue()}, detail: BodyNotJSON},
		{name: "an empty body as an object", body: "", opts: []ReadOption{EmptyBodyAsObject()}, raw: `{}`},
		{name: "white space as an object", body: " \n", opts: []ReadOption{EmptyBodyAsObject()}, raw: `{}`},
		{name: "white space as an object, first value", body: " \n", opts: []ReadOption{EmptyBodyAsObject(), FirstValue()}, raw: `{}`},
		{name: "the null is still refused", body: "null", opts: []ReadOption{EmptyBodyAsObject()}, detail: BodyMismatch},
		{name: "the null as an object", body: " null ", opts: []ReadOption{NullAsEmptyObject()}, raw: `{}`},
		{name: "the null as an object, first value", body: "null x", opts: []ReadOption{NullAsEmptyObject(), FirstValue()}, raw: `{}`},
		{name: "an empty body is not the null", body: "", opts: []ReadOption{NullAsEmptyObject()}, detail: BodyRequired},
	} {
		raw, refusal := ReadInput(strings.NewReader(tc.body), fields, tc.opts...)
		switch {
		case tc.detail == "" && (refusal != nil || string(raw) != tc.raw):
			t.Errorf("%s: read %s, %+v, want %s", tc.name, raw, refusal, tc.raw)
		case tc.detail != "" && (refusal == nil || refusal.Detail != tc.detail):
			t.Errorf("%s: read %s, %+v, want the detail %q", tc.name, raw, refusal, tc.detail)
		}
	}

	_, refusal := ReadInput(failingRead{io.ErrUnexpectedEOF}, fields, FirstValue())
	if refusal == nil || refusal.Detail != BodyNotJSON {
		t.Errorf("first value, a failed read: %+v, want %q", refusal, BodyNotJSON)
	}
	raw, refusal := ReadInputDroppingUnknown(strings.NewReader(""), fields, EmptyBodyAsObject())
	if refusal != nil || string(raw) != `{}` {
		t.Errorf("dropping unknown keys, an empty body as an object: %s, %+v", raw, refusal)
	}
}

// TestStrictTypeNamesTheType: a refused undeclared key of a StrictType
// read names the type, and a read without it names none.
func TestStrictTypeNamesTheType(t *testing.T) {
	_, refusal := ReadInput(strings.NewReader(`{"name":"a","extra":1}`), []string{"name"}, StrictType("Widget"))
	if refusal == nil || refusal.Type != "Widget" || refusal.Reason != "unknown fields: extra" {
		t.Errorf("a strict type: %+v", refusal)
	}
	_, refusal = ReadInput(strings.NewReader(`{"name":"a","extra":1}`), []string{"name"})
	if refusal == nil || refusal.Type != "" {
		t.Errorf("no strict type: %+v", refusal)
	}
}

func TestReadInputMakesTheChecksEveryServerMakes(t *testing.T) {
	fields := []string{"name", "slug"}
	for _, tc := range []struct {
		body, detail, reason, errors string
	}{
		{body: "", detail: BodyRequired},
		{body: " \n", detail: BodyRequired},
		{body: "{nope", detail: BodyNotJSON},
		{body: `{"name":"a"} {}`, detail: BodyNotJSON},
		{body: "null", detail: BodyMismatch, reason: "expected an object"},
		{body: "[1]", detail: BodyMismatch, reason: "expected an object"},
		{
			body:   `{"zeta":1,"name":"a","alpha":2,"zeta":3}`,
			detail: BodyMismatch,
			reason: "unknown fields: zeta, alpha",
			errors: `{"alpha":[{"validator":"unknown","message":"unknown field"}],"zeta":[{"validator":"unknown","message":"unknown field"}]}`,
		},
	} {
		raw, refusal := ReadInput(strings.NewReader(tc.body), fields)
		if refusal == nil {
			t.Errorf("%q: read %s, want a refusal", tc.body, raw)
			continue
		}
		if refusal.Detail != tc.detail || refusal.Reason != tc.reason {
			t.Errorf("%q: refusal %q / %q, want %q / %q", tc.body, refusal.Detail, refusal.Reason, tc.detail, tc.reason)
		}
		if tc.errors != "" {
			got, err := json.Marshal(refusal.Errors)
			if err != nil || string(got) != tc.errors {
				t.Errorf("%q: errors %s (%v), want %s", tc.body, got, err, tc.errors)
			}
		} else if refusal.Errors != nil {
			t.Errorf("%q: errors %v, want none", tc.body, refusal.Errors)
		}
	}

	raw, refusal := ReadInput(strings.NewReader(`{"name":"a","slug":{"x":[1]}}`), fields)
	if refusal != nil || string(raw) != `{"name":"a","slug":{"x":[1]}}` {
		t.Fatalf("a declared body: %s, %+v", raw, refusal)
	}
}

func TestReadInputDroppingUnknownKeepsOnlyTheDeclaredKeys(t *testing.T) {
	fields := []string{"webhook_type", "item_id"}
	for _, tc := range []struct{ body, detail, reason string }{
		{body: "", detail: BodyRequired},
		{body: "{nope", detail: BodyNotJSON},
		{body: "null", detail: BodyMismatch, reason: "expected an object"},
		{body: `["item_id"]`, detail: BodyMismatch, reason: "expected an object"},
	} {
		raw, refusal := ReadInputDroppingUnknown(strings.NewReader(tc.body), fields)
		if refusal == nil || refusal.Detail != tc.detail || refusal.Reason != tc.reason {
			t.Errorf("%q: read %s, refusal %+v, want %q / %q", tc.body, raw, refusal, tc.detail, tc.reason)
		}
	}
	for body, want := range map[string]string{
		`{"environment":"sandbox","webhook_type":"ITEM","Item_Id":"x","item_id":"a","extra":{"n":[1]}}`: `{"webhook_type":"ITEM","item_id":"a"}`,
		`{"environment":"sandbox"}`:                     `{}`,
		`{"item_id":"a","item_id":"b"}`:                 `{"item_id":"a","item_id":"b"}`,
		`{ "item_id" : { "nested" : [1, 2] } , "x":1 }`: `{"item_id":{ "nested" : [1, 2] }}`,
	} {
		raw, refusal := ReadInputDroppingUnknown(strings.NewReader(body), fields)
		if refusal != nil || string(raw) != want {
			t.Errorf("%s: read %s, %+v, want %s", body, raw, refusal, want)
		}
	}
}
