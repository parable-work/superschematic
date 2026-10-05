package sqlite

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
)

// SQLite's result codes the adapter reads, as ResultCode returns them.
const (
	// ResultBusy is SQLITE_BUSY: another connection holds the lock past
	// the busy timeout.
	ResultBusy = 5
	// ResultConstraintUnique is SQLITE_CONSTRAINT_UNIQUE: a unique index
	// refused a row.
	ResultConstraintUnique = 2067
)

// ResultCode reads SQLite's result code from an error a driver returned:
// the Code() int of the first error in its chain that has one, as
// modernc.org/sqlite's *sqlite.Error does. That driver turns SQLite's
// extended result codes on, so the code is the extended one (2067 for a
// unique index, not 19). It reports false for an error with no such method.
func ResultCode(err error) (int, bool) {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code(), true
	}
	return 0, false
}

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randRead fills an id's bytes: crypto/rand's Read, which a test swaps for a
// seeded generator (export_test.go) to write a file byte for byte.
var randRead = rand.Read

// newID returns a new version-4 UUID in its canonical form (base62).
func newID() string {
	var b [16]byte
	// crypto/rand's Read never fails, and always fills b.
	_, _ = randRead(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	n := new(big.Int).SetBytes(b[:])
	if n.Sign() == 0 {
		return "0"
	}
	var out []byte
	base, digit := big.NewInt(62), new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, base, digit)
		out = append(out, base62Alphabet[digit.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// maxExactMicros is the widest time, in microseconds either side of the
// Unix epoch, that every adapter reads exactly: 2^53 - 1, the largest
// integer a double holds exactly, which bounds what the TypeScript adapter
// reads.
const maxExactMicros = 1<<53 - 1

// exactTime refuses a time in microseconds outside ±maxExactMicros, which
// the TypeScript adapter cannot read, as it says of a column that holds one.
func exactTime(micros int64, column string) error {
	if micros > maxExactMicros || micros < -maxExactMicros {
		return fmt.Errorf("sqlite: column %s is %d, not an integer a number holds exactly", column, micros)
	}
	return nil
}

// microsToDateTime writes a time in microseconds since the Unix epoch as a
// canonical date-time: UTC with Z, its fraction of a second without
// trailing zeros and left out when zero. A year outside 0000-9999 is
// refused, and then a time outside ±maxExactMicros.
func microsToDateTime(micros int64) (string, error) {
	t := time.UnixMicro(micros).UTC()
	if t.Year() < 0 || t.Year() > 9999 {
		return "", fmt.Errorf("sqlite: %d microseconds falls outside the years 0000-9999", micros)
	}
	if micros > maxExactMicros || micros < -maxExactMicros {
		return "", fmt.Errorf("sqlite: %d is not a whole number of microseconds a number holds exactly", micros)
	}
	return t.Format(time.RFC3339Nano), nil
}

// jsonString writes a JSON string as the canonical rules do: `"` and `\`
// escaped, \b, \f, \n, \r and \t by name, every other control character as
// \u00xx in lowercase hex, and everything else as it is.
func jsonString(s string) string {
	const hex = "0123456789abcdef"
	var buf strings.Builder
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hex[c>>4])
				buf.WriteByte(hex[c&0xf])
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
	return buf.String()
}

// optionalJSONString is jsonString, or null when the value is absent.
func optionalJSONString(s string, present bool) string {
	if !present {
		return "null"
	}
	return jsonString(s)
}

// jsonTime writes a time as a canonical date-time's JSON string, or null
// when it is absent.
func jsonTime(micros int64, present bool) (string, error) {
	if !present {
		return "null", nil
	}
	text, err := microsToDateTime(micros)
	if err != nil {
		return "", err
	}
	return jsonString(text), nil
}

// writeObject writes a JSON object of members, each already JSON text,
// sorted by name: the order of their code points, which UTF-8's bytes keep.
func writeObject(members map[string]string) string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf strings.Builder
	buf.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(jsonString(name))
		buf.WriteByte(':')
		buf.WriteString(members[name])
	}
	buf.WriteByte('}')
	return buf.String()
}

// readObject reads a JSON object column, each member as its JSON text.
func readObject(text, column string) (map[string]string, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &members); err != nil || members == nil {
		return nil, fmt.Errorf("column %s does not hold a JSON object", column)
	}
	out := make(map[string]string, len(members))
	for name, value := range members {
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err != nil {
			return nil, fmt.Errorf("column %s: %w", column, err)
		}
		out[name] = compact.String()
	}
	return out, nil
}

// canonicalOf is the canonical JSON text of a value of a class.
func canonicalOf(class string, value json.RawMessage) (string, error) {
	out, err := canonical.Postgres(class, value)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// canonicalText is the canonical JSON text of a value of a class given as a
// string: an id, an entity key, an actor or a time.
func canonicalText(class, value string) (string, error) {
	return canonicalOf(class, json.RawMessage(jsonString(value)))
}
