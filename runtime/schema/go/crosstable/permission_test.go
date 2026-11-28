package crosstable

import (
	"context"
	"testing"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// TestASourcesPermissionIsTheSchemasUnlessTheOptionsName (#601): a source
// whose definition names a permission is read only by a caller who holds
// it; CalendarOptions.Permissions naming the source wins over the
// schema's.
func TestASourcesPermissionIsTheSchemasUnlessTheOptionsName(t *testing.T) {
	defs := &Definitions{Calendar: map[string]CalendarTable{
		"Filing": {Table: "filing", Key: "id", KeyType: "uuid", UUID: true, Sources: []CalendarSource{{Name: "filing", Kind: "deadline", Title: "title", Permission: "filings.read",
			Occurrences: &CalendarOccurrences{Table: "filing_recurrence_occurrences", Row: "filing_id", Key: "key", Due: "due", DueAt: "due_at", State: "state"}}}},
	}}
	principal := access.Principal{Subject: "00000000-0000-4000-8000-0000000000a1", Kind: access.KindUser}
	read := CalendarRead{From: "2027-01-01T00:00:00Z", To: "2027-06-01T00:00:00Z"}
	for _, test := range []struct {
		name        string
		permissions []string
		options     CalendarOptions
		reads       bool
	}{
		{"the schema's, not held", nil, CalendarOptions{}, false},
		{"the schema's, held", []string{"filings.read"}, CalendarOptions{}, true},
		{"the option's in its place, held", []string{"calendar.all"}, CalendarOptions{Permissions: map[string]string{"filing": "calendar.all"}}, true},
		{"the option's in its place, the schema's alone held", []string{"filings.read"}, CalendarOptions{Permissions: map[string]string{"filing": "calendar.all"}}, false},
	} {
		h := &recordHost{defs: defs}
		got, err := ReadCalendar(context.Background(), h.host(), Caller{Principal: principal, Permissions: test.permissions}, read, test.options)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if reads := len(h.statements) > 0; reads != test.reads {
			t.Errorf("%s: read the source %v, want %v (%d entries)", test.name, reads, test.reads, len(got.Entries))
		}
	}
}
