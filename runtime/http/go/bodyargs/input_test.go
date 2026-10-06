package bodyargs

import (
	"encoding/json"
	"strings"
	"testing"
)

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
