package crosstable

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"
)

var update = flag.Bool("update", false, "rewrite calendar_parity.json's wants from this runtime's answers")

// calendarParityPath is the calendar's vectors, which the TypeScript and
// Rust runtimes run too (#616).
var calendarParityPath = filepath.Join("..", "..", "..", "http", "testdata", "calendar_parity.json")

type calendarParity struct {
	Comment    string `json:"comment"`
	References []struct {
		Name  string        `json:"name"`
		Entry CalendarEntry `json:"entry"`
		Want  string        `json:"want"`
	} `json:"references"`
	Feeds []struct {
		Name         string          `json:"name"`
		Entries      []CalendarEntry `json:"entries"`
		Detail       CalendarDetail  `json:"detail"`
		Now          int64           `json:"now"`
		CalendarName string          `json:"calendarName"`
		Want         string          `json:"want"`
	} `json:"feeds"`
}

// TestCalendarParityVectors runs calendar_parity.json (#616), which the
// TypeScript runtime's calendar-parity.test.ts and the Rust runtime's
// orm::calendar tests run too: a busy entry's reference number, and the
// iCalendar text of entries in full and busy. With -update it rewrites the
// wants from this runtime's answers, the file kept ASCII.
func TestCalendarParityVectors(t *testing.T) {
	data, err := os.ReadFile(calendarParityPath)
	if err != nil {
		t.Fatal(err)
	}
	var file calendarParity
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.References) == 0 || len(file.Feeds) == 0 {
		t.Fatal("calendar_parity.json holds no references or no feeds")
	}
	for i, tc := range file.References {
		got := CalendarReference(tc.Entry)
		if *update {
			file.References[i].Want = got
		} else if got != tc.Want {
			t.Errorf("%s: CalendarReference = %s, want %s", tc.Name, got, tc.Want)
		}
	}
	for i, tc := range file.Feeds {
		got := CalendarICS(tc.Entries, tc.Detail, time.UnixMilli(tc.Now), tc.CalendarName)
		if *update {
			file.Feeds[i].Want = got
		} else if got != tc.Want {
			t.Errorf("%s: CalendarICS =\n%q\nwant\n%q", tc.Name, got, tc.Want)
		}
	}
	if *update {
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(file); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(calendarParityPath, asciiJSON(out.Bytes()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// asciiJSON writes every character past ASCII in JSON text as its \u
// escape (a pair of them past the Basic Multilingual Plane), which reads
// back as the same string.
func asciiJSON(text []byte) []byte {
	var out bytes.Buffer
	for _, r := range string(text) {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r > 0xFFFF:
			high, low := utf16.EncodeRune(r)
			fmt.Fprintf(&out, `\u%04x\u%04x`, high, low)
		default:
			fmt.Fprintf(&out, `\u%04x`, r)
		}
	}
	return out.Bytes()
}
