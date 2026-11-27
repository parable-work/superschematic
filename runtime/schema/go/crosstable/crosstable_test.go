package crosstable

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// TestSearchWords: a query's words are its runs of characters that are
// not white space, marks dropped, the first sixteen, each at most 200
// characters, as the TypeScript runtime's searchWords reads them.
func TestSearchWords(t *testing.T) {
	if got := SearchWords("  walnut\tSink \u0002x\u0003  "); !reflect.DeepEqual(got, []string{"walnut", "Sink", "x"}) {
		t.Errorf("SearchWords = %q", got)
	}
	if got := SearchWords(strings.Repeat("w ", 20)); len(got) != MaxSearchWords {
		t.Errorf("%d words count, want %d", len(got), MaxSearchWords)
	}
	if got := SearchWords(strings.Repeat("é", 250)); len([]rune(got[0])) != 200 {
		t.Errorf("a long word keeps %d characters", len([]rune(got[0])))
	}
	if got := SearchWords(" \n "); got != nil {
		t.Errorf("a blank query: %q", got)
	}
}

// TestSnippetParts: ts_headline's text, with the marks around each match,
// is parts whose matches are marked, and no mark is in any part.
func TestSnippetParts(t *testing.T) {
	got := snippetParts("Bolts and a \u0002walnut\u0003 trim, \u0002walnut\u0003")
	want := []SnippetPart{{Text: "Bolts and a "}, {Text: "walnut", Match: true}, {Text: " trim, "}, {Text: "walnut", Match: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snippetParts = %+v", got)
	}
	if got := snippetParts("open \u0002ended"); !reflect.DeepEqual(got, []SnippetPart{{Text: "open "}, {Text: "ended", Match: true}}) {
		t.Errorf("an unclosed match: %+v", got)
	}
}

// TestFeedScope: a feed's scope is its grant's target type and back, and
// only a CalendarFeed target is a feed's.
func TestFeedScope(t *testing.T) {
	scope := CalendarFeedScope{Detail: DetailFull, Sources: []string{"filing", "contract"}}
	if got := scope.TargetType(); got != "CalendarFeed;detail=full;sources=filing,contract" {
		t.Errorf("TargetType = %q", got)
	}
	if got := FeedScopeOf(scope.TargetType()); !reflect.DeepEqual(got, scope) {
		t.Errorf("FeedScopeOf = %+v", got)
	}
	if got := FeedScopeOf("CalendarFeed"); got.Detail != DetailBusy || got.Sources != nil {
		t.Errorf("a bare feed reads %+v, want busy and every source", got)
	}
	for target, want := range map[string]bool{"CalendarFeed": true, "CalendarFeed;detail=busy": true, "CalendarFeeds": false, "Contract": false} {
		if IsCalendarFeed(target) != want {
			t.Errorf("IsCalendarFeed(%q) = %t", target, !want)
		}
	}
}

// TestCalendarICS: an entry is a VEVENT, an all-day one by its date; a
// full feed carries its title, kind and state, a busy one "Busy" and its
// reference alone; a long line folds at 75 octets.
func TestCalendarICS(t *testing.T) {
	entries := []CalendarEntry{
		{Source: "filing", Kind: "deadline", Type: "Filing", Key: "k1", Title: "Quarterly return; VAT, " + strings.Repeat("long ", 20), Start: "2026-10-31T09:00:00.000Z", Occurrence: &CalendarOccurrence{Key: "2026-10-31T09:00", State: "skipped"}},
		{Source: "document", Kind: "document", Type: "Document", Key: "k2", Title: "Plan", Start: "2026-11-02", End: "2026-11-03", AllDay: true},
	}
	if got := CalendarReference(entries[0]); got != "ZYMVQF37" {
		t.Errorf("the first reference is %s", got)
	}
	if got := CalendarReference(entries[1]); got != "4Q87423H" {
		t.Errorf("the second reference is %s", got)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	full := CalendarICS(entries, DetailFull, now, "")
	for _, want := range []string{"BEGIN:VCALENDAR\r\n", "X-WR-CALNAME:Calendar\r\n", "UID:ZYMVQF37-2026-10-31T09:00@superschematic\r\n", "DTSTAMP:20260102T030405Z\r\n", "DTSTART:20261031T090000Z\r\n", "SUMMARY:Quarterly return\\; VAT\\, long", "STATUS:CANCELLED\r\n", "DTSTART;VALUE=DATE:20261102\r\n", "DTEND;VALUE=DATE:20261103\r\n", "CATEGORIES:document\r\n", "END:VCALENDAR\r\n"} {
		if !strings.Contains(full, want) {
			t.Errorf("the full feed lacks %q:\n%s", want, full)
		}
	}
	for _, line := range strings.Split(full, "\r\n") {
		if len(line) > 75 {
			t.Errorf("a line of %d octets: %q", len(line), line)
		}
	}
	if !strings.Contains(full, "\r\n ") {
		t.Error("the long summary is not folded")
	}
	busy := CalendarICS(entries, DetailBusy, now, "")
	if strings.Contains(busy, "Quarterly") || strings.Contains(busy, "Plan") || !strings.Contains(busy, "SUMMARY:Busy (4Q87423H)\r\nTRANSP:OPAQUE") {
		t.Errorf("the busy feed:\n%s", busy)
	}
}

// TestInstants: a window's bound is an instant, a date or epoch
// milliseconds; any other is Invalid.
func TestInstants(t *testing.T) {
	for text, want := range map[string]time.Time{
		"2026-01-02T03:04:05.678Z":  time.Date(2026, 1, 2, 3, 4, 5, 678e6, time.UTC),
		"2026-01-02T04:04:05+01:00": time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"2026-01-02":                time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		"1767323045000":             time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	} {
		got, err := instant(text, "from")
		if err != nil || !got.Equal(want) {
			t.Errorf("instant(%q) = %v, %v", text, got, err)
		}
	}
	var bad Invalid
	if _, err := instant("soon", "to"); !errors.As(err, &bad) || err.Error() != "readCalendar: to is no instant: soon" {
		t.Errorf("instant(soon): %v", err)
	}
}

// TestAudienceConditions: a user sees an index row whose audience is
// everyone or holds them; an agent run what whom it acts for sees, and
// not a row closed to runs; a caller with no UUID what everyone sees.
func TestAudienceConditions(t *testing.T) {
	var args []any
	user := audienceConditions("si.audience", "si.agents", access.Principal{Subject: "00000000-0000-4000-8000-0000000000a1"}, &args)
	if user[0] != "(si.audience IS NULL OR si.audience && ARRAY[$1::uuid]::uuid[])" || len(args) != 1 {
		t.Errorf("a user's: %q %v", user, args)
	}
	args = []any{"q"}
	agent := audienceConditions("si.audience", "si.agents", access.Principal{Subject: "3cKSm7Pv2EM8Wl4z4XvMh1", Kind: access.KindAgent, ActingFor: &access.ActingFor{Subject: "00000000-0000-4000-8000-0000000000a1"}}, &args)
	if agent[0] != "(si.audience IS NULL OR si.audience && ARRAY[$2::uuid, $3::uuid]::uuid[]) AND si.agents <> 'none'" || len(args) != 3 {
		t.Errorf("an agent run's: %q %v", agent, args)
	}
	args = nil
	if anonymous := audienceConditions("si.audience", "si.agents", access.Principal{}, &args); anonymous[0] != "si.audience IS NULL" || len(args) != 0 {
		t.Errorf("an anonymous caller's: %q", anonymous)
	}
}
