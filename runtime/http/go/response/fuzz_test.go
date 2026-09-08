package response

import (
	"strings"
	"testing"
)

func FuzzUnwrapEnvelope(f *testing.F) {
	f.Add([]byte(`{"data": {"id": "123"}, "meta": {"requestId": "abc"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`invalid json`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`"hello"`))
	f.Add([]byte(`42`))
	f.Add([]byte(`{"data":null,"meta":{"requestId":"abc"}}`))
	f.Add([]byte(`{"error":"bad request"}`))
	f.Add([]byte(`{"data":{"id":"x"}}`))
	f.Add([]byte(`{"data":{"id":"x"},"meta":"string"}`))
	f.Add([]byte(`{"data":{"id":"x"},"meta":null}`))
	f.Add([]byte(`{"data":{"id":"x"},"meta":{"other":"val"}}`))
	f.Add([]byte(`{"data":[1,2,3],"meta":{"requestId":"abc"}}`))
	f.Add([]byte(`{"data":"hello","meta":{"requestId":"abc"}}`))
	f.Add([]byte(`{"data":true,"meta":{"requestId":"abc"}}`))
	f.Add([]byte(`{"data":42,"meta":{"requestId":"abc"}}`))
	f.Add([]byte(strings.Repeat("{", 1000)))
	f.Add([]byte(strings.Repeat("a", 100000)))
	f.Add([]byte("'; DROP TABLE users; --"))
	f.Add([]byte("<script>alert('xss')</script>"))
	f.Add([]byte("\x00\x01\x02\x03"))
	f.Add([]byte("\xff\xfe\xfd"))

	f.Fuzz(func(t *testing.T, input []byte) {
		result, err := UnwrapEnvelope(input)
		if err == nil && result == nil {
			t.Fatal("expected non-nil data when unwrap succeeds")
		}
	})
}
