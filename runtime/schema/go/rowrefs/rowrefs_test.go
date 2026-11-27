package rowrefs

import (
	"strings"
	"testing"
)

// TestKeysReadEitherFormAndWriteBase62: a UUID key is read hyphenated or
// in base62, the Go types' and the TypeScript runtime's wire form, and
// written back in base62; a text key is left as it is.
func TestKeysReadEitherFormAndWriteBase62(t *testing.T) {
	const text = "00000000-0000-4000-8000-0000000000a1"
	wire := UUIDFromDB(text)
	if wire == text || strings.Contains(wire, "-") {
		t.Fatalf("UUIDFromDB(%s) = %s", text, wire)
	}
	if got := UUIDToDB(wire); got != text {
		t.Fatalf("UUIDToDB(%s) = %s, want %s", wire, got, text)
	}
	if got := UUIDToDB(text); got != text {
		t.Fatalf("UUIDToDB of the hyphenated form = %s", got)
	}
	for _, c := range []struct{ uuid, base62 string }{
		{"00000000-0000-0000-0000-000000000000", "0"},
		{"ffffffff-ffff-ffff-ffff-ffffffffffff", "7n42DGM5Tflk9n8mt7Fhc7"},
	} {
		if got := UUIDFromDB(c.uuid); got != c.base62 {
			t.Errorf("UUIDFromDB(%s) = %s, want %s", c.uuid, got, c.base62)
		}
	}
	if key, err := dbKey(false, "sku-1"); err != nil || key != "sku-1" {
		t.Fatalf("a text key: %s, %v", key, err)
	}
	if _, err := dbKey(true, "not a uuid!"); CodeOf(err) != CodeInvalid {
		t.Fatalf("a key that is no UUID: %v", err)
	}
	if !sameSubject(text, wire) || sameSubject(text, "other") {
		t.Fatal("sameSubject")
	}
	if !strings.HasPrefix(newUUID(), "") || UUIDToDB(newUUID()) == "" {
		t.Fatal("newUUID is no UUID")
	}
}

func TestMediaTypesAndNames(t *testing.T) {
	if !mediaTypeAllowed([]string{"text/*", "application/pdf"}, "text/plain") || mediaTypeAllowed([]string{"text/*"}, "application/zip") || !mediaTypeAllowed(nil, "application/zip") {
		t.Fatal("mediaTypeAllowed")
	}
	for _, name := range []string{"", " ", "a/b", "a\\b", "a\x00b", strings.Repeat("x", 256)} {
		if CodeOf(checkName(name)) != CodeInvalid {
			t.Errorf("the name %q is not refused", name)
		}
	}
	if err := checkName("plan.txt"); err != nil {
		t.Fatal(err)
	}
}
