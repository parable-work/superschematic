package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalJSON re-encodes raw in canonical form: compact, object members
// sorted by key, array order and number literals kept as written, and <, >
// and & in strings escaped. It is what encoding/json writes for a value
// decoded with UseNumber into any. Only whitespace may follow the value.
//
// It is a copy of ir.CanonicalJSON in the compiler, which writes the hashes
// this package checks; the two must not drift.
func CanonicalJSON(raw []byte) ([]byte, error) {
	value, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// decode reads one JSON value with UseNumber and refuses anything but
// whitespace after it.
func decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	// Decoder.More reports false before a closing bracket or brace, so it
	// would pass `{}]`; read the rest of the input instead.
	if len(bytes.TrimLeft(raw[dec.InputOffset():], " \t\r\n")) > 0 {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return value, nil
}

// Hash returns the lowercase hex SHA-256 of canonical, which the caller has
// already put in canonical form.
func Hash(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// PlanHash returns the hash of a plan document: the SHA-256 of the canonical
// JSON of the plan object without its hash member.
func PlanHash(doc []byte) (string, error) {
	value, err := decode(doc)
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a plan is a JSON object")
	}
	return planHash(object)
}

func planHash(object map[string]any) (string, error) {
	without := make(map[string]any, len(object))
	for key, value := range object {
		if key != "hash" {
			without[key] = value
		}
	}
	canonical, err := json.Marshal(without)
	if err != nil {
		return "", err
	}
	return Hash(canonical), nil
}

// LockKey is the Postgres advisory lock key of a service's runs: the first
// eight bytes, big-endian, of the SHA-256 of "superschematic_migrate:"
// followed by the service.
func LockKey(service string) int64 {
	sum := sha256.Sum256([]byte("superschematic_migrate:" + service))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// sqlHash is the hash a log row records for a step's statements: the
// SHA-256 of the statements joined by "\n;\n".
func sqlHash(statements []string) string {
	var joined bytes.Buffer
	for i, statement := range statements {
		if i > 0 {
			joined.WriteString("\n;\n")
		}
		joined.WriteString(statement)
	}
	return Hash(joined.Bytes())
}
