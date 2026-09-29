package postgres

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// canonicalUUID returns the canonical form (base62) of a UUID Postgres
// rendered as text, or of one already canonical.
func canonicalUUID(text string) (string, error) {
	quoted, err := json.Marshal(text)
	if err != nil {
		return "", err
	}
	out, err := canonical.Postgres(canonical.UUID, quoted)
	if err != nil {
		return "", err
	}
	var id string
	if err := json.Unmarshal(out, &id); err != nil {
		return "", err
	}
	return id, nil
}

// uuidText returns the hyphenated form Postgres reads of a UUID given in
// its canonical form, or hyphenated.
func uuidText(id string) (string, error) {
	canonicalID, err := canonicalUUID(id)
	if err != nil {
		return "", err
	}
	n := new(big.Int)
	for _, c := range []byte(canonicalID) {
		n.Mul(n, big.NewInt(62))
		n.Add(n, big.NewInt(int64(strings.IndexByte(base62Alphabet, c))))
	}
	hex := fmt.Sprintf("%032x", n)
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32], nil
}

// optionalUUIDText is uuidText, with "" for "".
func optionalUUIDText(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	return uuidText(id)
}

// uuidTexts is uuidText over a list.
func uuidTexts(ids []string) ([]string, error) {
	out := make([]string, len(ids))
	for i, id := range ids {
		text, err := uuidText(id)
		if err != nil {
			return nil, err
		}
		out[i] = text
	}
	return out, nil
}

// optionalCanonicalUUID is canonicalUUID, with "" for "".
func optionalCanonicalUUID(text string) (string, error) {
	if text == "" {
		return "", nil
	}
	return canonicalUUID(text)
}

// inputValue turns a canonical value of class into the JSON
// jsonb_populate_record reads into the column: a UUID, or a list of them,
// hyphenated, and a duration, or a list of them, as interval text. A json
// column and a list of lists are JSONB and keep the canonical JSON, which is
// the schema runtime's; every other class is already what Postgres reads.
func inputValue(class string, value json.RawMessage) (json.RawMessage, error) {
	normalized, err := canonical.Postgres(class, value)
	if err != nil {
		return nil, err
	}
	element, depth := class, 0
	if strings.HasSuffix(class, "[][]") {
		return normalized, nil
	} else if strings.HasSuffix(class, "[]") {
		element, depth = strings.TrimSuffix(class, "[]"), 1
	}
	var convert func(string) (string, error)
	switch element {
	case canonical.UUID:
		convert = uuidText
	case canonical.Duration:
		convert = intervalText
	default:
		return normalized, nil
	}
	if bytes.Equal(normalized, []byte("null")) {
		return normalized, nil
	}
	if depth == 0 {
		var s string
		if err := json.Unmarshal(normalized, &s); err != nil {
			return nil, err
		}
		out, err := convert(s)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	var list []string
	if err := json.Unmarshal(normalized, &list); err != nil {
		return nil, err
	}
	for i, s := range list {
		if list[i], err = convert(s); err != nil {
			return nil, err
		}
	}
	return json.Marshal(list)
}

// intervalText writes a canonical duration as interval text Postgres reads
// exactly: [-]H:MM:SS with the fraction of a second.
func intervalText(duration string) (string, error) {
	d, err := time.ParseDuration(duration)
	if err != nil {
		return "", fmt.Errorf("duration %q: %w", duration, err)
	}
	sign := ""
	nanos := d.Nanoseconds()
	if nanos < 0 {
		sign, nanos = "-", -nanos
	}
	seconds, fraction := nanos/int64(time.Second), nanos%int64(time.Second)
	text := fmt.Sprintf("%s%02d:%02d:%02d", sign, seconds/3600, seconds/60%60, seconds%60)
	if fraction > 0 {
		text += strings.TrimRight(fmt.Sprintf(".%09d", fraction), "0")
	}
	return text, nil
}
