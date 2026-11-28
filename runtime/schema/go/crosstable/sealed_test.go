package crosstable

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/fieldcrypt"
)

// sealedHost answers a field source's read with one row whose title is
// the envelope it holds, and a read of an occurrences source's rows with
// one occurrence of that row, and holds keys when keys is set.
type sealedHost struct {
	*recordHost
	title string
}

type keyedHost struct {
	sealedHost
	keys *fieldcrypt.Keyring
}

func (h keyedHost) FieldKeys() *fieldcrypt.Keyring { return h.keys }

const sealedRow = "00000000-0000-4000-8000-000000000003"

func (h sealedHost) Query(_ context.Context, sql string, _ ...any) ([]Row, error) {
	switch {
	case strings.Contains(sql, "AS due_at"):
		return []Row{{"row": sealedRow, "key": "2027-03-01T09:00", "due": "2027-03-01T09:00", "due_at": "2027-03-01T09:00:00.000Z", "state": "upcoming"}}, nil
	case strings.Contains(sql, "AS start"):
		return []Row{{"key": sealedRow, "title": h.title, "start": "2027-02-01", "finish": nil}}, nil
	case strings.Contains(sql, "AS title"):
		return []Row{{"key": sealedRow, "title": h.title}}, nil
	}
	return nil, nil
}

// TestASealedTitleOpensThroughTheHostsKeys (#616): a calendar source
// titled by an Encrypted<T> field reads the field's envelope and opens it
// under the host's keys, bound to the table type, the field and the row's
// key, for a field source and an occurrences source alike; a host with no
// keys, and an envelope of another row, fail the read.
func TestASealedTitleOpensThroughTheHostsKeys(t *testing.T) {
	keys, err := fieldcrypt.ParseKeys(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keys.Seal(fieldcrypt.Cell{Table: "Matter", Field: "name", Row: sealedRow}, "Estate of a client")
	if err != nil {
		t.Fatal(err)
	}
	other, err := keys.Seal(fieldcrypt.Cell{Table: "Matter", Field: "name", Row: "00000000-0000-4000-8000-000000000004"}, "Another")
	if err != nil {
		t.Fatal(err)
	}
	defs := &Definitions{Calendar: map[string]CalendarTable{
		"Matter": {Table: "matter", Key: "id", KeyType: "uuid", UUID: true, Sources: []CalendarSource{
			{Name: "matter", Kind: "hearing", Title: "name", TitleSealed: true, TitleField: "name", Field: "heard_on"},
			{Name: "matter.occurrences", Kind: "deadline", Title: "name", TitleSealed: true, TitleField: "name",
				Occurrences: &CalendarOccurrences{Table: "matter_recurrence_occurrences", Row: "matter_id", Key: "key", Due: "due", DueAt: "due_at", State: "state"}},
		}},
	}}
	caller := Caller{Principal: access.Principal{Subject: "00000000-0000-4000-8000-0000000000a1", Kind: access.KindUser}}
	read := CalendarRead{From: "2027-01-01T00:00:00Z", To: "2027-06-01T00:00:00Z"}
	host := keyedHost{sealedHost{&recordHost{defs: defs}, envelope}, keys}
	got, err := ReadCalendar(context.Background(), host, caller, read, CalendarOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries %+v, want the row's and its occurrence's", got.Entries)
	}
	for _, entry := range got.Entries {
		if entry.Title != "Estate of a client" {
			t.Errorf("%s's title %q, want the field opened", entry.Source, entry.Title)
		}
	}
	if _, err := ReadCalendar(context.Background(), sealedHost{&recordHost{defs: defs}, envelope}, caller, read, CalendarOptions{}); !errors.Is(err, fieldcrypt.ErrNoKeys) {
		t.Errorf("a host with no keys read %v, want ErrNoKeys", err)
	}
	if _, err := ReadCalendar(context.Background(), keyedHost{sealedHost{&recordHost{defs: defs}, other}, keys}, caller, read, CalendarOptions{}); !errors.Is(err, fieldcrypt.ErrDecrypt) {
		t.Errorf("another row's envelope read %v, want ErrDecrypt", err)
	}
	// A plain title is read as it is.
	plain := &Definitions{Calendar: map[string]CalendarTable{"Matter": {Table: "matter", Key: "id", KeyType: "uuid", UUID: true, Sources: []CalendarSource{{Name: "matter", Kind: "hearing", Title: "name", Field: "heard_on"}}}}}
	if got, err := ReadCalendar(context.Background(), sealedHost{&recordHost{defs: plain}, "Plain"}, caller, read, CalendarOptions{}); err != nil || len(got.Entries) != 1 || got.Entries[0].Title != "Plain" {
		t.Errorf("a plain title: %+v, %v", got.Entries, err)
	}
}
