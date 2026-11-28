package rowrefs

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

var update = flag.Bool("update", false, "rewrite runtime/http/testdata/attachments_parity.json")

// attachmentsParityPath holds the rules of a file of a row (D154) that the
// Go and TypeScript runtimes both run: a file's name, a media type, and a
// declaration's mediaTypes admitting one.
var attachmentsParityPath = filepath.Join("..", "..", "..", "http", "testdata", "attachments_parity.json")

type nameVector struct {
	Name string `json:"name"`
	Text string `json:"text"`
	Want bool   `json:"want"`
}

type mediaTypeVector struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Want      bool   `json:"want"`
}

type allowedVector struct {
	Name      string   `json:"name"`
	Allowed   []string `json:"allowed"`
	MediaType string   `json:"mediaType"`
	Want      bool     `json:"want"`
}

const attachmentsParityComment = "The rules of a file of a row (D154) that Go's rowrefs (runtime/schema/go) and TypeScript's rowfeatures (runtime/http/typescript) both apply. " +
	"names: whether text is a file's name, 1 to 255 Unicode code points (D14, amended: lengths count code points, not UTF-16 units), " +
	"none of them /, \\ or a control character (U+0000 to U+001F, U+007F), and not Unicode white space alone. " +
	"mediaTypes: whether mediaType is one a file may have, a lowercase type/subtype without parameters. " +
	"allowed: whether a declaration's mediaTypes (type/subtype, type/* or */*; none admits any) admit mediaType. " +
	"runtime/schema/go/rowrefs writes it with go test -run TestAttachmentsParityVectors -update."

var (
	emoji = "\U0001F600"
	// e and a combining acute accent: two code points, one character as
	// read.
	accented = "e\u0301"
)

var nameVectors = []nameVector{
	{"an ASCII name", "plan.pdf", true},
	{"a name outside ASCII", "\u00dcberweisung 2026.csv", true},
	{"one character", "a", true},
	{"empty", "", false},
	{"255 ASCII characters", strings.Repeat("a", 255), true},
	{"256 ASCII characters", strings.Repeat("a", 256), false},
	{"200 emoji, 400 UTF-16 units", strings.Repeat(emoji, 200), true},
	{"255 emoji", strings.Repeat(emoji, 255), true},
	{"256 emoji", strings.Repeat(emoji, 256), false},
	{"127 accented letters, 254 code points", strings.Repeat(accented, 127), true},
	{"128 accented letters, 256 code points", strings.Repeat(accented, 128), false},
	{"254 characters and an emoji", strings.Repeat("a", 254) + emoji, true},
	{"a slash", "notes/plan.pdf", false},
	{"a backslash", "notes\\plan.pdf", false},
	{"a NUL", "plan\x00.pdf", false},
	{"a tab", "plan\t.pdf", false},
	{"a newline", "plan\n.pdf", false},
	{"U+001F", "plan\x1f.pdf", false},
	{"DEL", "plan\x7f.pdf", false},
	{"a C1 control character is no control character here", "plan\u0080.pdf", true},
	{"a space alone", " ", false},
	{"spaces around a name", " plan.pdf ", true},
	{"a no-break space alone", "\u00a0", false},
	{"a next-line character alone", "\u0085", false},
	{"an ideographic space alone", "\u3000", false},
	{"a line separator alone", "\u2028", false},
	{"a zero-width no-break space is no white space", "\ufeff", true},
	{"dots", "..", true},
}

var mediaTypeVectors = []mediaTypeVector{
	{"a type and subtype", "application/pdf", true},
	{"a suffix", "image/svg+xml", true},
	{"the characters a token may hold", "application/vnd.a-b_c!#$&^", true},
	{"upper case", "Text/plain", false},
	{"a parameter", "text/plain; charset=utf-8", false},
	{"no subtype", "text", false},
	{"an empty subtype", "text/", false},
	{"an empty type", "/plain", false},
	{"two slashes", "text/plain/x", false},
	{"a 127-character subtype", "x/" + strings.Repeat("a", 127), true},
	{"a 128-character subtype", "x/" + strings.Repeat("a", 128), false},
	{"a 127-character type", strings.Repeat("a", 127) + "/x", true},
	{"a 128-character type", strings.Repeat("a", 128) + "/x", false},
	{"a subtype that opens with a symbol", "text/+plain", false},
	{"a letter outside ASCII", "text/pl\u00e4in", false},
	{"a trailing newline", "text/plain\n", false},
}

var allowedVectors = []allowedVector{
	{"no declaration admits any", nil, "image/png", true},
	{"an empty list admits any", []string{}, "image/png", true},
	{"the same type", []string{"image/png"}, "image/png", true},
	{"another subtype", []string{"image/png"}, "image/jpeg", false},
	{"a family", []string{"text/*", "image/*"}, "image/jpeg", true},
	{"a family is its whole type", []string{"text/*"}, "textual/plain", false},
	{"a family is no prefix of a type", []string{"te/*"}, "text/plain", false},
	{"any type", []string{"*/*"}, "application/pdf", true},
	{"a family matches no other family", []string{"text/*"}, "image/png", false},
	{"no wildcard subtype but the whole", []string{"image/p*"}, "image/png", false},
	{"one of several", []string{"application/pdf", "text/csv"}, "text/csv", true},
}

// TestAttachmentsParityVectors holds checkName, the media type rule and
// mediaTypeAllowed to each vector's want and keeps attachments_parity.json,
// which the TypeScript runtime's attachments-parity.test.ts runs, equal to
// the vectors. A normal run fails when the file is stale; -update rewrites
// it.
func TestAttachmentsParityVectors(t *testing.T) {
	for _, v := range nameVectors {
		if got := checkName(v.Text) == nil; got != v.Want {
			t.Errorf("name %s: valid = %v, want %v", v.Name, got, v.Want)
		}
	}
	for _, v := range mediaTypeVectors {
		if got := mediaType.MatchString(v.MediaType); got != v.Want {
			t.Errorf("media type %s: valid = %v, want %v", v.Name, got, v.Want)
		}
	}
	for _, v := range allowedVectors {
		if got := mediaTypeAllowed(v.Allowed, v.MediaType); got != v.Want {
			t.Errorf("allowed %s: %v, want %v", v.Name, got, v.Want)
		}
	}

	allowed := make([]allowedVector, len(allowedVectors))
	for i, v := range allowedVectors {
		if v.Allowed == nil {
			v.Allowed = []string{}
		}
		allowed[i] = v
	}
	file := struct {
		Comment    string            `json:"comment"`
		Names      []nameVector      `json:"names"`
		MediaTypes []mediaTypeVector `json:"mediaTypes"`
		Allowed    []allowedVector   `json:"allowed"`
	}{attachmentsParityComment, nameVectors, mediaTypeVectors, allowed}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		t.Fatal(err)
	}
	// Plain ASCII: every other character is a \u escape.
	var ascii bytes.Buffer
	for _, r := range data.String() {
		switch {
		case r < 0x80:
			ascii.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&ascii, `\u%04x`, r)
		default:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&ascii, `\u%04x\u%04x`, hi, lo)
		}
	}
	if *update {
		if err := os.WriteFile(attachmentsParityPath, ascii.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(attachmentsParityPath)
	if err != nil {
		t.Fatalf("read %s: %v (run go test -run TestAttachmentsParityVectors -update)", attachmentsParityPath, err)
	}
	if !bytes.Equal(committed, ascii.Bytes()) {
		t.Fatalf("%s is stale; run go test -run TestAttachmentsParityVectors -update", attachmentsParityPath)
	}
}
