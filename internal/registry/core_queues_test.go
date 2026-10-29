package registry

import (
	"errors"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestQueueSettingsOf: a queue's arguments with their defaults (D53). An
// unset maximum backoff is an hour, or the backoff when that is longer, and
// a retention of zero keeps no done message, which unset is a week.
func TestQueueSettingsOf(t *testing.T) {
	two := 2
	for _, tc := range []struct {
		name string
		def  *ir.QueueDef
		want QueueSettings
	}{
		{"no queue", nil, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"no arguments", &ir.QueueDef{}, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"every argument", &ir.QueueDef{Retries: &two, Backoff: "10s", MaxBackoff: "5m", Lease: "2m", Retain: "24h"},
			QueueSettings{Retries: 2, BackoffSeconds: 10, MaxBackoffSeconds: 300, LeaseSeconds: 120, RetainSeconds: 86400}},
		{"a backoff over the default cap", &ir.QueueDef{Backoff: "2h"}, QueueSettings{Retries: 5, BackoffSeconds: 7200, MaxBackoffSeconds: 7200, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"a fixed backoff", &ir.QueueDef{Backoff: "1m", MaxBackoff: "1m"}, QueueSettings{Retries: 5, BackoffSeconds: 60, MaxBackoffSeconds: 60, LeaseSeconds: 60, RetainSeconds: 604800}},
		{"no retention", &ir.QueueDef{Retain: "0s"}, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 0}},
		{"no retention, bare", &ir.QueueDef{Retain: "0"}, QueueSettings{Retries: 5, BackoffSeconds: 30, MaxBackoffSeconds: 3600, LeaseSeconds: 60, RetainSeconds: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.def != nil {
				if err := CheckQueue(tc.def); err != nil {
					t.Fatalf("CheckQueue: %v", err)
				}
			}
			if got := QueueSettingsOf(tc.def); got != tc.want {
				t.Errorf("QueueSettingsOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCheckQueueRefusesAMaxBackoffUnderTheBackoffAndANegativeRetention:
// what CheckQueue refuses of the new arguments, in every form.
func TestCheckQueueRefusesAMaxBackoffUnderTheBackoffAndANegativeRetention(t *testing.T) {
	for _, tc := range []struct {
		def  ir.QueueDef
		want string
	}{
		{ir.QueueDef{Backoff: "1m", MaxBackoff: "59s"}, `maxBackoff is "59s", shorter than the backoff of 60s`},
		{ir.QueueDef{MaxBackoff: "29s"}, `maxBackoff is "29s", shorter than the backoff of 30s`},
		{ir.QueueDef{MaxBackoff: "0s"}, `maxBackoff: "0s" is not positive`},
		{ir.QueueDef{MaxBackoff: "1.5m"}, ""},
		{ir.QueueDef{MaxBackoff: "90500ms"}, `maxBackoff: "90500ms" is not a whole number of seconds`},
		{ir.QueueDef{Retain: "-1s"}, `retain: "-1s" is negative`},
		{ir.QueueDef{Retain: "7d"}, `retain: "7d" is no duration`},
		{ir.QueueDef{Retain: "168h"}, ""},
	} {
		err := CheckQueue(&tc.def)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("CheckQueue(%+v) = %v, want nil", tc.def, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("CheckQueue(%+v) = %v, want %q", tc.def, err, tc.want)
		}
	}
}

// TestCheckQueueKeyOnDuplicateAndSerialize: what CheckQueue takes and
// refuses of a queue's key, onDuplicate and serialize (D80). A key takes
// effect with replace, skip or serialize, and each of them needs one.
func TestCheckQueueKeyOnDuplicateAndSerialize(t *testing.T) {
	for _, tc := range []struct {
		def  ir.QueueDef
		want string
	}{
		{ir.QueueDef{Key: "orderId", OnDuplicate: "replace"}, ""},
		{ir.QueueDef{Key: "orderId", OnDuplicate: "skip"}, ""},
		{ir.QueueDef{Key: "orderId", Serialize: true}, ""},
		{ir.QueueDef{Key: "orderId", OnDuplicate: "append", Serialize: true}, ""},
		{ir.QueueDef{OnDuplicate: "append"}, ""},
		{ir.QueueDef{OnDuplicate: "merge"}, `onDuplicate is "merge"; it is "append", "replace" or "skip"`},
		{ir.QueueDef{OnDuplicate: "replace"}, `onDuplicate is "replace", which needs a key`},
		{ir.QueueDef{Serialize: true}, "serialize needs a key"},
		{ir.QueueDef{Key: "orderId"}, `key "orderId" takes effect with onDuplicate "replace" or "skip", or with serialize`},
		{ir.QueueDef{Key: "orderId", OnDuplicate: "append"}, `key "orderId" takes effect`},
	} {
		err := CheckQueue(&tc.def)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("CheckQueue(%+v) = %v, want nil", tc.def, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("CheckQueue(%+v) = %v, want %q", tc.def, err, tc.want)
		}
	}
	def := &ir.QueueDef{Key: "orderId", OnDuplicate: "replace", Serialize: true}
	if !def.Dedupes() || !def.Serializes() {
		t.Errorf("%+v dedupes %t and serializes %t, want both", def, def.Dedupes(), def.Serializes())
	}
	if (&ir.QueueDef{Key: "orderId", Serialize: true}).Dedupes() {
		t.Error("a serialized queue that appends dedupes")
	}
}

// TestQueueOfReadsKeyOnDuplicateAndSerialize: @queue reads the three
// options, and refuses one of the wrong type at its argument.
func TestQueueOfReadsKeyOnDuplicateAndSerialize(t *testing.T) {
	td := &ir.TypeDef{Name: "OrderChanged", Fields: []*ir.FieldDef{{Name: "orderId"}}}
	def, err := queueOf(td, []any{map[string]any{"key": "orderId", "onDuplicate": "replace", "serialize": true}})
	if err != nil {
		t.Fatal(err)
	}
	if def.Key != "orderId" || def.OnDuplicate != "replace" || !def.Serialize {
		t.Errorf("queueOf = %+v", def)
	}
	_, err = queueOf(td, []any{map[string]any{"serialize": "yes"}})
	var argErr *ArgError
	if err == nil || !errors.As(err, &argErr) || !strings.Contains(err.Error(), "serialize a boolean") {
		t.Errorf("queueOf with serialize \"yes\" = %v, want an ArgError naming serialize", err)
	}
}

// TestEnqueueOnChangeOf reads @enqueueOnChange's queue and options, its
// writes in their one order, and refuses each malformed one with what to
// write instead (D80).
func TestEnqueueOnChangeOf(t *testing.T) {
	queue := map[string]any{"class": "EmbedLineItem"}
	def, err := enqueueOnChangeOf([]any{queue, map[string]any{
		"on":      []any{"update", "insert"},
		"fields":  []any{"category", "subCategory"},
		"when":    []any{map[string]any{"field": "category", "notNull": true}, map[string]any{"field": "status", "equals": "open"}},
		"message": map[string]any{"lineItemId": "id"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if def.Queue != "EmbedLineItem" || strings.Join(def.On, ",") != "insert,update" || strings.Join(def.Fields, ",") != "category,subCategory" {
		t.Errorf("enqueueOnChangeOf = %+v", def)
	}
	if len(def.When) != 2 || !def.When[0].NotNull || def.When[1].Equals != "open" || def.Message["lineItemId"] != "id" {
		t.Errorf("enqueueOnChangeOf's when and message = %+v, %v", def.When, def.Message)
	}
	for _, tc := range []struct {
		options map[string]any
		want    string
	}{
		{map[string]any{"message": map[string]any{"a": "id"}}, "on names no write"},
		{map[string]any{"on": []any{"upsert"}, "message": map[string]any{"a": "id"}}, `on names "upsert"`},
		{map[string]any{"on": []any{"insert", "insert"}, "message": map[string]any{"a": "id"}}, `on names "insert" twice`},
		{map[string]any{"on": []any{"insert"}, "fields": []any{"category"}, "message": map[string]any{"a": "id"}}, "on names no update"},
		{map[string]any{"on": []any{"insert"}}, "message maps no field"},
		{map[string]any{"on": []any{"insert"}, "message": map[string]any{"a": 1.0}}, "name a field of the table"},
		{map[string]any{"on": []any{"insert"}, "when": []any{map[string]any{"field": "a"}}, "message": map[string]any{"a": "id"}}, "sets 0 of isNull, notNull and equals"},
		{map[string]any{"on": []any{"insert"}, "when": []any{map[string]any{"field": "a", "isNull": true, "equals": "x"}}, "message": map[string]any{"a": "id"}}, "sets 2 of"},
		{map[string]any{"on": []any{"insert"}, "when": []any{map[string]any{"field": "a", "isNull": false}}, "message": map[string]any{"a": "id"}}, "write isNull: true"},
		{map[string]any{"on": []any{"insert"}, "when": []any{map[string]any{"field": "a", "equals": nil}}, "message": map[string]any{"a": "id"}}, "write isNull: true"},
		{map[string]any{"on": []any{"insert"}, "message": map[string]any{"a": "id"}, "where": "x"}, `has no option "where"`},
		{map[string]any{"on": []any{"insert"}, "message": map[string]any{"a": "id"}, "backfill": "yes"}, "backfill is true or false"},
		{map[string]any{"on": []any{"delete"}, "message": map[string]any{"a": "id"}, "backfill": true}, "on names neither insert nor update"},
	} {
		_, err := enqueueOnChangeOf([]any{queue, tc.options})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("enqueueOnChangeOf(%v) = %v, want %q", tc.options, err, tc.want)
		}
	}
	if _, err := enqueueOnChangeOf([]any{"EmbedLineItem", map[string]any{}}); err == nil || !strings.Contains(err.Error(), "takes the @queue class") {
		t.Errorf("enqueueOnChangeOf of a string queue = %v", err)
	}
	backfill, err := enqueueOnChangeOf([]any{queue, map[string]any{"on": []any{"insert"}, "message": map[string]any{"a": "id"}, "backfill": true}})
	if err != nil || !backfill.Backfill {
		t.Errorf("enqueueOnChangeOf with backfill (D88) = %+v, %v", backfill, err)
	}
}

// TestRoleSeedsOf reads @seedRoles's roles in order, an empty list of
// permissions among them, and refuses each malformed one (D88).
func TestRoleSeedsOf(t *testing.T) {
	seeds, err := roleSeedsOf([]any{[]any{
		map[string]any{"name": "member", "permissions": []any{"orders.read", "orders.write"}},
		map[string]any{"name": "guest", "permissions": []any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 2 || seeds[0].Name != "member" || strings.Join(seeds[0].Permissions, ",") != "orders.read,orders.write" ||
		seeds[1].Name != "guest" || seeds[1].Permissions == nil || len(seeds[1].Permissions) != 0 {
		t.Errorf("roleSeedsOf = %+v", seeds)
	}
	for _, tc := range []struct {
		args []any
		want string
	}{
		{nil, "takes one list of roles"},
		{[]any{[]any{}}, "a list of one or more roles"},
		{[]any{[]any{"member"}}, "write { name, permissions }"},
		{[]any{[]any{map[string]any{"name": "member"}}}, "needs a name and permissions"},
		{[]any{[]any{map[string]any{"name": 1.0, "permissions": []any{}}}}, "name is 1; it is a string"},
		{[]any{[]any{map[string]any{"name": "member", "permissions": []any{1.0}}}}, "permissions: want a list of strings"},
		{[]any{[]any{map[string]any{"name": "member", "permissions": []any{}, "grants": true}}}, `has no key "grants"`},
	} {
		if _, err := roleSeedsOf(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("roleSeedsOf(%v) = %v, want %q", tc.args, err, tc.want)
		}
	}
}
