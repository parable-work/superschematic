package rules

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// The conversions between the core's JSON and a column's value: the UUID
// codec (D79) both ways, Date.prototype.toISOString's text, and each row of
// the bind and read tables.

func TestUUIDCodec(t *testing.T) {
	cases := []struct{ wire, column string }{
		{"0", "00000000-0000-0000-0000-000000000000"},
		{"7n42DGM5Tflk9n8mt7Fhc7", "ffffffff-ffff-ffff-ffff-ffffffffffff"},
		// rules.test.ts's key.
		{"6fe1XoZ0UJpMNNdUOSMC2X", "db360f43-5530-c407-56da-9cd8e0c971dd"},
	}
	for _, c := range cases {
		column, err := uuidToColumn(c.wire)
		if err != nil || column != c.column {
			t.Errorf("uuidToColumn(%q) = %q, %v; want %q", c.wire, column, err, c.column)
		}
		wire, err := uuidToWire(c.column)
		if err != nil || wire != c.wire {
			t.Errorf("uuidToWire(%q) = %q, %v; want %q", c.column, wire, err, c.wire)
		}
		// Each form reads as itself too, and upper-case hex reads.
		if again, _ := uuidToWire(c.wire); again != c.wire {
			t.Errorf("uuidToWire(%q) = %q", c.wire, again)
		}
		if again, _ := uuidToColumn(strings.ToUpper(c.column)); again != c.column {
			t.Errorf("uuidToColumn(upper %q) = %q", c.column, again)
		}
	}
	for _, bad := range []string{
		"",
		"7n42DGM5Tflk9n8mt7Fhc8",                // past the largest 128-bit value
		"00000000000000000000000",               // 23 characters
		"db360f43-5530-c407-56da-9cd8e0c971d",   // 35 characters, hyphenated
		"db360f43_5530-c407-56da-9cd8e0c971dd",  // no hyphen in its place
		"db360f43-5530-c407-56da-9cd8e0c971dz",  // no hex digit
		"6fe1XoZ0UJp-NNdUOSMC2X",                // no base62 digit
		"db360f4355301c407156da19cd8e0c971dd00", // 37 characters
	} {
		if got, err := uuidToColumn(bad); err == nil {
			t.Errorf("uuidToColumn(%q) = %q, want a refusal", bad, got)
		}
	}
	raw := randomUUID()
	if raw[6]>>4 != 4 || raw[8]>>6 != 2 {
		t.Errorf("randomUUID() = %x, want a version 4 variant 1 UUID", raw)
	}
	wire := base62(new(big.Int).SetBytes(raw[:]))
	if column, err := uuidToColumn(wire); err != nil || column != hyphenatedBytes(raw) {
		t.Errorf("a random UUID's wire form %q reads as %q, %v", wire, column, err)
	}
}

func TestISOText(t *testing.T) {
	cases := map[int64]string{
		0:             "1970-01-01T00:00:00.000Z",
		1234:          "1970-01-01T00:00:01.234Z",
		1767225600001: "2026-01-01T00:00:00.001Z",
	}
	for millis, want := range cases {
		if got := isoText(time.UnixMilli(millis)); got != want {
			t.Errorf("isoText(%d) = %q, want %q", millis, got, want)
		}
	}
	if got := isoText(time.UnixMilli(1000).In(time.FixedZone("X", 3600))); got != "1970-01-01T00:00:01.000Z" {
		t.Errorf("an instant in another zone writes %q", got)
	}
}

var convertTable = &Table{Type: "Order"}

func TestToColumn(t *testing.T) {
	text := Column{Field: "title", Column: "title", Type: "string"}
	stamp := Column{Field: "statusEnteredAt", Column: "status_entered_at", Type: "Temporal.DateTime", Convert: ConvertEpochMillis}
	id := Column{Field: "ownerId", Column: "owner_id", Type: "Identity.UUID"}
	data := Column{Field: "data", Column: "data", Type: "Generic.JSON"}
	flag := Column{Field: "done", Column: "done", Type: "boolean"}
	number := Column{Field: "n", Column: "n", Type: "number"}
	cases := []struct {
		raw      string
		column   Column
		postgres any
		sqlite   any
	}{
		{"null", text, nil, nil},
		{"", text, nil, nil},
		{"1000", stamp, time.UnixMilli(1000).UTC(), "1970-01-01T00:00:01.000Z"},
		{`"1000"`, stamp, time.UnixMilli(1000).UTC(), "1970-01-01T00:00:01.000Z"},
		{"1000.9", stamp, time.UnixMilli(1000).UTC(), "1970-01-01T00:00:01.000Z"},
		{`"6fe1XoZ0UJpMNNdUOSMC2X"`, id, "db360f43-5530-c407-56da-9cd8e0c971dd", "db360f43-5530-c407-56da-9cd8e0c971dd"},
		{`{"a": [1, 2]}`, data, `{"a":[1,2]}`, `{"a":[1,2]}`},
		{`[1, "x"]`, data, `[1,"x"]`, `[1,"x"]`},
		{`"123"`, data, `"123"`, `"123"`},
		{`true`, data, `true`, `true`},
		{`7`, data, `7`, `7`},
		{"true", flag, true, int64(1)},
		{"false", flag, false, int64(0)},
		{"42", number, int64(42), int64(42)},
		{"-7", number, int64(-7), int64(-7)},
		{"1.5", number, 1.5, 1.5},
		{"1e3", number, 1000.0, 1000.0},
		{"92233720368547758070", number, 92233720368547758070.0, 92233720368547758070.0},
		{`"Desk"`, text, "Desk", "Desk"},
		{`"6fe1XoZ0UJpMNNdUOSMC2X"`, text, "6fe1XoZ0UJpMNNdUOSMC2X", "6fe1XoZ0UJpMNNdUOSMC2X"},
	}
	for _, c := range cases {
		for _, dialect := range []Dialect{DialectPostgres, DialectSQLite} {
			want := c.postgres
			if dialect == DialectSQLite {
				want = c.sqlite
			}
			got, err := toColumn(json.RawMessage(c.raw), c.column, convertTable, dialect)
			if err != nil {
				t.Errorf("%s: toColumn(%s, %s) = %v", dialect, c.raw, c.column.Type, err)
				continue
			}
			if wantTime, ok := want.(time.Time); ok {
				gotTime, ok := got.(time.Time)
				if !ok || !gotTime.Equal(wantTime) || gotTime.Location() != time.UTC {
					t.Errorf("%s: toColumn(%s, %s) = %#v, want %v in UTC", dialect, c.raw, c.column.Type, got, wantTime)
				}
				continue
			}
			if got != want {
				t.Errorf("%s: toColumn(%s, %s) = %#v, want %#v", dialect, c.raw, c.column.Type, got, want)
			}
		}
	}
	_, err := toColumn(json.RawMessage(`"later"`), stamp, convertTable, DialectPostgres)
	if refusal, ok := AsRefusal(err); !ok || refusal.Code != CodeInternal || refusal.Message != `rules: Order.statusEnteredAt: "later" is no epoch milliseconds` {
		t.Errorf("a timestamp that is no number = %v", err)
	}
	if _, err := toColumn(json.RawMessage(`"o-1"`), id, convertTable, DialectPostgres); err == nil {
		t.Error("a UUID column bound a value that is no UUID")
	}
}

func TestFromColumn(t *testing.T) {
	text := Column{Field: "title", Column: "title", Type: "string"}
	stamp := Column{Field: "statusEnteredAt", Column: "status_entered_at", Type: "Temporal.DateTime", Convert: ConvertEpochMillis}
	id := Column{Field: "ownerId", Column: "owner_id", Type: "Identity.UUID"}
	data := Column{Field: "data", Column: "data", Type: "Generic.JSON"}
	flag := Column{Field: "done", Column: "done", Type: "boolean"}
	number := Column{Field: "n", Column: "n", Type: "number"}
	var uuidBytes [16]byte
	copy(uuidBytes[:], []byte{0xdb, 0x36, 0x0f, 0x43, 0x55, 0x30, 0xc4, 0x07, 0x56, 0xda, 0x9c, 0xd8, 0xe0, 0xc9, 0x71, 0xdd})
	cases := []struct {
		value  any
		column Column
		want   string
	}{
		{time.UnixMilli(1234), stamp, "1234"},
		{"1970-01-01T00:00:01.234Z", stamp, "1234"},
		{"1970-01-01 00:00:01.234+00:00", stamp, "1234"},
		{"1970-01-01T01:00:01+01:00", stamp, "1000"},
		{"1970-01-01 00:00:01.5", stamp, "1500"},
		{[]byte("1970-01-01T00:00:02Z"), stamp, "2000"},
		{int64(77), stamp, "77"},
		{uuidBytes, id, `"6fe1XoZ0UJpMNNdUOSMC2X"`},
		{"db360f43-5530-c407-56da-9cd8e0c971dd", id, `"6fe1XoZ0UJpMNNdUOSMC2X"`},
		{[]byte("db360f43-5530-c407-56da-9cd8e0c971dd"), id, `"6fe1XoZ0UJpMNNdUOSMC2X"`},
		{int64(1), flag, "true"},
		{int64(0), flag, "false"},
		{true, flag, "true"},
		{time.UnixMilli(1000), text, `"1970-01-01T00:00:01.000Z"`},
		{map[string]any{"a": 1.0}, data, `{"a":1}`},
		{[]any{"x", 2.0}, data, `["x",2]`},
		{`{"a": 1}`, data, `{"a":1}`},
		{[]byte(`[1,2]`), data, `[1,2]`},
		{"not json", data, `"not json"`},
		{int64(42), number, "42"},
		{int32(-3), number, "-3"},
		{uint8(200), number, "200"},
		{1.5, number, "1.5"},
		{float32(2.5), number, "2.5"},
		{json.Number("12.50"), number, "12.50"},
		{"Desk", text, `"Desk"`},
		{[]byte("Desk"), text, `"Desk"`},
	}
	for _, c := range cases {
		got, present, err := fromColumn(c.value, c.column, convertTable)
		if err != nil || !present || string(got) != c.want {
			t.Errorf("fromColumn(%#v, %s) = %s, %v, %v; want %s", c.value, c.column.Type, got, present, err, c.want)
		}
	}
	if _, present, err := fromColumn(nil, text, convertTable); present || err != nil {
		t.Errorf("a NULL column is present (%v, %v)", present, err)
	}
	_, _, err := fromColumn("yesterday", stamp, convertTable)
	if refusal, ok := AsRefusal(err); !ok || refusal.Message != `rules: Order.statusEnteredAt: "yesterday" is no timestamp` {
		t.Errorf("text that is no timestamp = %v", err)
	}
	if _, _, err := fromColumn("o-1", id, convertTable); err == nil {
		t.Error("a UUID column read a value that is no UUID")
	}
}

func TestTimeOfMillis(t *testing.T) {
	for _, absent := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(" null ")} {
		if got, err := TimeOfMillis(absent); got != nil || err != nil {
			t.Errorf("TimeOfMillis(%q) = %v, %v; want nil", absent, got, err)
		}
	}
	got, err := TimeOfMillis(json.RawMessage("1767225600001"))
	if err != nil || got == nil || !got.Equal(time.UnixMilli(1767225600001)) || got.Location() != time.UTC {
		t.Errorf("TimeOfMillis = %v, %v", got, err)
	}
	if _, err := TimeOfMillis(json.RawMessage(`{"at": 1}`)); err == nil {
		t.Error("an object read as epoch milliseconds")
	}
}
