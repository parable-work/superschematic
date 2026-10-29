package fieldcrypt

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update rewrites the parity vectors from this package's sealing.
var update = flag.Bool("update", false, "rewrite runtime/schema/testdata/fieldcrypt_parity.json")

// vectorsPath holds the envelopes both runtimes read (D85): this package,
// and the TypeScript HTTP runtime's fieldcrypt.test.ts.
var vectorsPath = filepath.Join("..", "..", "testdata", "fieldcrypt_parity.json")

type sealVector struct {
	Name      string `json:"name"`
	Keyring   string `json:"keyring"`
	Table     string `json:"table"`
	Field     string `json:"field"`
	Row       string `json:"row"`
	Plaintext string `json:"plaintext"`
	Nonce     string `json:"nonce"`
	Envelope  string `json:"envelope"`
}

type openVector struct {
	Name      string  `json:"name"`
	Keyring   *string `json:"keyring"`
	Table     string  `json:"table"`
	Field     string  `json:"field"`
	Row       string  `json:"row"`
	Envelope  string  `json:"envelope"`
	Plaintext string  `json:"plaintext,omitempty"`
	Error     string  `json:"error,omitempty"`
}

type keyringVector struct {
	Keyring string   `json:"keyring"`
	IDs     []string `json:"ids,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type vectors struct {
	Comment  string          `json:"comment"`
	Keyrings []keyringVector `json:"keyrings"`
	Seals    []sealVector    `json:"seals"`
	Opens    []openVector    `json:"opens"`
}

func fixedKey(start byte) string {
	raw := make([]byte, KeySize)
	for i := range raw {
		raw[i] = start + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func fixedNonce(start byte) []byte {
	nonce := make([]byte, NonceSize)
	for i := range nonce {
		nonce[i] = start + byte(i)
	}
	return nonce
}

// errorKind names err as the vectors do.
func errorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNoKeys):
		return "no-keys"
	case errors.Is(err, ErrBadKeys):
		return "bad-keys"
	case errors.Is(err, ErrUnknownKey):
		return "unknown-key"
	case errors.Is(err, ErrDecrypt):
		return "decrypt"
	case errors.Is(err, ErrMalformed):
		return "malformed"
	}
	return "other: " + err.Error()
}

// buildVectors seals the vectors' envelopes with this package.
func buildVectors(t *testing.T) vectors {
	current, previous := fixedKey(1), fixedKey(101)
	both := current + ", " + previous
	ring := func(secret string) *Keyring {
		k, err := ParseKeys(secret)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	row := "0b9c4d8e-5f1a-4c2b-9d3e-7a6b5c4d3e2f"
	seal := func(name, secret, table, field, row, plaintext string, nonce []byte) sealVector {
		envelope, err := ring(secret).seal(Cell{table, field, row}, plaintext, nonce)
		if err != nil {
			t.Fatal(err)
		}
		return sealVector{name, secret, table, field, row, plaintext, base64.RawURLEncoding.EncodeToString(nonce), envelope}
	}
	token := seal("a Plaid access token", both, "PlaidItem", "accessToken", row, "access-sandbox-8ab976e3-8c64-4ff1-9f71-a3e5f9d1b2c0", fixedNonce(7))
	empty := seal("the empty text", current, "PlaidItem", "accessToken", row, "", fixedNonce(30))
	unicode := seal("text past ASCII", current, "Note", "body", "42", "caf\u00e9 \u2603 \U0001f511", fixedNonce(60))
	old := seal("under the previous key", previous, "PlaidItem", "accessToken", row, "access-sandbox-old", fixedNonce(90))
	tampered := []byte(token.Envelope)
	last := len(tampered) - 1
	if tampered[last] == 'A' {
		tampered[last] = 'B'
	} else {
		tampered[last] = 'A'
	}
	none := (*string)(nil)
	str := func(s string) *string { return &s }
	return vectors{
		Comment: "Encrypted<T> envelopes (D85): runtime/schema/go/fieldcrypt writes them (go test -run TestParityVectors -update) and runtime/http/typescript/src/fieldcrypt.test.ts reads them.",
		Keyrings: []keyringVector{
			{Keyring: current, IDs: []string{ring(current).CurrentID()}},
			{Keyring: both, IDs: []string{ring(current).CurrentID(), ring(previous).CurrentID()}},
			{Keyring: strings.TrimRight(current, "="), IDs: []string{ring(current).CurrentID()}},
			{Keyring: "", Error: "bad-keys"},
			{Keyring: current + ",", Error: "bad-keys"},
			{Keyring: "not base64!", Error: "bad-keys"},
			{Keyring: base64.StdEncoding.EncodeToString(make([]byte, 16)), Error: "bad-keys"},
			{Keyring: current + "," + current, Error: "bad-keys"},
		},
		Seals: []sealVector{token, empty, unicode, old},
		Opens: []openVector{
			{Name: "the current key", Keyring: str(both), Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: token.Envelope, Plaintext: token.Plaintext},
			{Name: "a previous key, listed after the current one", Keyring: str(both), Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: old.Envelope, Plaintext: old.Plaintext},
			{Name: "a key the keyring does not hold", Keyring: str(current), Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: old.Envelope, Error: "unknown-key"},
			{Name: "another row", Keyring: str(both), Table: "PlaidItem", Field: "accessToken", Row: "1b9c4d8e-5f1a-4c2b-9d3e-7a6b5c4d3e2f", Envelope: token.Envelope, Error: "decrypt"},
			{Name: "another field", Keyring: str(both), Table: "PlaidItem", Field: "itemId", Row: row, Envelope: token.Envelope, Error: "decrypt"},
			{Name: "another table", Keyring: str(both), Table: "Account", Field: "accessToken", Row: row, Envelope: token.Envelope, Error: "decrypt"},
			{Name: "a changed ciphertext", Keyring: str(both), Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: string(tampered), Error: "decrypt"},
			{Name: "plaintext", Keyring: str(both), Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: "access-sandbox-plain", Error: "malformed"},
			{Name: "another version", Keyring: str(both), Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: "v2" + strings.TrimPrefix(token.Envelope, "v1"), Error: "malformed"},
			{Name: "a short nonce", Keyring: str(both), Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: "v1:" + ring(current).CurrentID() + ":AAAA:" + strings.Split(token.Envelope, ":")[3], Error: "malformed"},
			{Name: "no keyring", Keyring: none, Table: "PlaidItem", Field: "accessToken", Row: row, Envelope: token.Envelope, Error: "no-keys"},
		},
	}
}

// TestParityVectors holds this package to the vectors both runtimes read,
// and rewrites them with -update.
func TestParityVectors(t *testing.T) {
	built := buildVectors(t)
	if *update {
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(built); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vectorsPath, asciiJSON([]byte(buf.String())), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("%v; run go test -run TestParityVectors -update", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range v.Keyrings {
		ring, err := ParseKeys(k.Keyring)
		if kind := errorKind(err); kind != k.Error {
			t.Errorf("keyring %q: error %q, want %q", k.Keyring, kind, k.Error)
			continue
		}
		if err != nil {
			continue
		}
		var ids []string
		for _, part := range strings.Split(k.Keyring, ",") {
			raw, _ := decodeKey(strings.TrimSpace(part))
			ids = append(ids, KeyID(raw))
		}
		if strings.Join(ids, ",") != strings.Join(k.IDs, ",") || ring.CurrentID() != k.IDs[0] {
			t.Errorf("keyring %q: ids %v, current %s, want %v", k.Keyring, ids, ring.CurrentID(), k.IDs)
		}
	}
	for _, s := range v.Seals {
		ring, err := ParseKeys(s.Keyring)
		if err != nil {
			t.Fatal(err)
		}
		nonce, err := base64.RawURLEncoding.DecodeString(s.Nonce)
		if err != nil {
			t.Fatal(err)
		}
		cell := Cell{s.Table, s.Field, s.Row}
		envelope, err := ring.seal(cell, s.Plaintext, nonce)
		if err != nil || envelope != s.Envelope {
			t.Errorf("%s: sealed %q (%v), want %q", s.Name, envelope, err, s.Envelope)
		}
		if plain, err := ring.Open(cell, s.Envelope); err != nil || plain != s.Plaintext {
			t.Errorf("%s: opened %q (%v), want %q", s.Name, plain, err, s.Plaintext)
		}
	}
	for _, o := range v.Opens {
		var ring *Keyring
		if o.Keyring != nil {
			if ring, err = ParseKeys(*o.Keyring); err != nil {
				t.Fatal(err)
			}
		}
		plain, err := ring.Open(Cell{o.Table, o.Field, o.Row}, o.Envelope)
		if kind := errorKind(err); kind != o.Error || plain != o.Plaintext {
			t.Errorf("%s: opened %q, error %q; want %q, error %q", o.Name, plain, kind, o.Plaintext, o.Error)
		}
	}
	if len(v.Seals) != len(built.Seals) || len(v.Opens) != len(built.Opens) || len(v.Keyrings) != len(built.Keyrings) {
		t.Errorf("the vectors are not this package's; run go test -run TestParityVectors -update")
	}
}

// asciiJSON escapes every rune of JSON text past ASCII, as \uXXXX.
func asciiJSON(data []byte) []byte {
	var b strings.Builder
	for _, r := range string(data) {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(&b, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			fmt.Fprintf(&b, "\\u%04x", r)
		}
	}
	return []byte(b.String())
}

// TestSealIsRandom: two seals of one value differ, and each opens.
func TestSealIsRandom(t *testing.T) {
	ring, err := ParseKeys(fixedKey(1))
	if err != nil {
		t.Fatal(err)
	}
	cell := Cell{"PlaidItem", "accessToken", "row"}
	a, err := ring.Seal(cell, "secret")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ring.Seal(cell, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two seals are the same envelope %q", a)
	}
	if !ring.Current(a) || !strings.HasPrefix(a, strings.TrimSuffix(ring.CurrentPrefix(), "%")) {
		t.Errorf("%q is not current under %s", a, ring.CurrentPrefix())
	}
	for _, e := range []string{a, b} {
		if plain, err := ring.Open(cell, e); err != nil || plain != "secret" {
			t.Errorf("open %q: %q, %v", e, plain, err)
		}
	}
}

// TestErrorNamesTheCellAlone: an error names the table and the field, and
// never the value or the key.
func TestErrorNamesTheCellAlone(t *testing.T) {
	ring, err := ParseKeys(fixedKey(1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ring.Open(Cell{"PlaidItem", "accessToken", "row"}, "access-sandbox-plain")
	var fe *Error
	if !errors.As(err, &fe) || fe.Table != "PlaidItem" || fe.Field != "accessToken" || !errors.Is(err, ErrMalformed) {
		t.Fatalf("error %#v", err)
	}
	if msg := err.Error(); strings.Contains(msg, "access-sandbox-plain") || !strings.Contains(msg, "PlaidItem.accessToken") {
		t.Errorf("message %q", msg)
	}
	_, err = ParseKeys("not base64!")
	if msg := err.Error(); strings.Contains(msg, "not base64!") {
		t.Errorf("the keys' message holds the key: %q", msg)
	}
}

func TestNewRowKeyIsAVersion4UUID(t *testing.T) {
	a, b := NewRowKey(), NewRowKey()
	if a == b || a[6]>>4 != 4 || a[8]>>6 != 2 {
		t.Fatalf("row keys %x and %x", a, b)
	}
}
