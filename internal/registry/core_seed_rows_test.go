package registry

import (
	"strings"
	"testing"
)

// TestSeedRowsOf reads @seedRows's rows in order, with null, lists and
// objects among the values, and its key, and refuses each malformed
// declaration (#625).
func TestSeedRowsOf(t *testing.T) {
	seeds, err := seedRowsOf([]any{
		[]any{
			map[string]any{"principal": nil, "eventKind": "*", "channels": []any{"sms", "email"}},
			map[string]any{"principal": nil, "eventKind": "ticket.overdue", "channels": []any{}, "rule": map[string]any{"every": 2.0}},
		},
		map[string]any{"key": []any{"principal", "eventKind"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds.Rows) != 2 || seeds.Rows[0]["eventKind"] != "*" || seeds.Rows[1]["eventKind"] != "ticket.overdue" ||
		strings.Join(seeds.Key, ",") != "principal,eventKind" {
		t.Errorf("seedRowsOf = %+v", seeds)
	}
	if v, ok := seeds.Rows[0]["principal"]; !ok || v != nil {
		t.Errorf("a null value is not kept: %+v", seeds.Rows[0])
	}
	if seeds, err := seedRowsOf([]any{[]any{map[string]any{"channelKind": "sms"}}}); err != nil || seeds.Key != nil {
		t.Errorf("seedRowsOf with no options = %+v, %v; want no key", seeds, err)
	}
	for _, tc := range []struct {
		args []any
		want string
	}{
		{nil, "takes a list of rows"},
		{[]any{[]any{}}, "a list of one or more rows"},
		{[]any{"sms"}, "a list of one or more rows"},
		{[]any{[]any{"sms"}}, "write an object of the row's fields"},
		{[]any{[]any{map[string]any{}}}, "write an object of the row's fields"},
		{[]any{[]any{map[string]any{"queue": map[string]any{"class": "Work"}}}}, "names a class"},
		{[]any{[]any{map[string]any{"a": 1.0}}, "key"}, "write { key"},
		{[]any{[]any{map[string]any{"a": 1.0}}, map[string]any{"key": []any{}}}, "a list of one or more"},
		{[]any{[]any{map[string]any{"a": 1.0}}, map[string]any{"key": "a"}}, "a list of one or more"},
		{[]any{[]any{map[string]any{"a": 1.0}}, map[string]any{"unique": []any{"a"}}}, `no option "unique"`},
		{[]any{[]any{map[string]any{"a": 1.0}}, map[string]any{}, 3.0}, "takes a list of rows"},
	} {
		if _, err := seedRowsOf(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("seedRowsOf(%v) = %v, want %q", tc.args, err, tc.want)
		}
	}
}
