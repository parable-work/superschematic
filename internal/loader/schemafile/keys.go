package schemafile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// pointerEscaper escapes an object key as a JSON pointer token (RFC 6901).
var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// refuseRepeatedKeys rejects a payload in which an object repeats a key,
// naming the JSON pointer of the repeated member. The slot check and the
// JSON Schema validator read the last copy of a repeated key, while the
// strict decode reads every copy into the same map or struct, so an earlier
// copy would reach the IR without being validated. Keys compare as decoded,
// after escapes are read, as the decoder compares them.
func refuseRepeatedKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return refuseRepeatedKeysAt(dec, "")
}

// refuseRepeatedKeysAt reads the value at pointer from dec.
func refuseRepeatedKeysAt(dec *json.Decoder, pointer string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := tok.(string)
			if !ok {
				return fmt.Errorf("object key at '%s' is not a string", pointer)
			}
			member := pointer + "/" + pointerEscaper.Replace(key)
			if seen[key] {
				return fmt.Errorf("repeated object key %q at '%s'", key, member)
			}
			seen[key] = true
			if err := refuseRepeatedKeysAt(dec, member); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			if err := refuseRepeatedKeysAt(dec, pointer+"/"+strconv.Itoa(i)); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	// The closing delimiter.
	_, err = dec.Token()
	return err
}
