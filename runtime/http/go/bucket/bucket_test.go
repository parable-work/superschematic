package bucket

import (
	"strings"
	"testing"
	"time"
)

func TestSignedURLOptionsCheck(t *testing.T) {
	for _, tc := range []struct {
		opts SignedURLOptions
		want string
	}{
		{SignedURLOptions{Method: MethodGet, Expires: time.Minute}, ""},
		{SignedURLOptions{Method: MethodPut, Expires: MaxSignedURLExpiry, ContentType: "image/png"}, ""},
		{SignedURLOptions{Method: "DELETE", Expires: time.Minute}, `method is "DELETE"`},
		{SignedURLOptions{Method: MethodGet}, "expires after 0s"},
		{SignedURLOptions{Method: MethodPut, Expires: MaxSignedURLExpiry + time.Second}, "at most 168h0m0s"},
		{SignedURLOptions{Method: MethodGet, Expires: time.Minute, ContentType: "image/png"}, "names a content type"},
	} {
		err := tc.opts.Check()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%+v: %v", tc.opts, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%+v: got %v, want an error with %q", tc.opts, err, tc.want)
		}
	}
}

func TestCheckName(t *testing.T) {
	for name, ok := range map[string]bool{
		"products/7b0e/image.png": true,
		"":                        false,
		strings.Repeat("a", 1025): false,
		"line\nbreak":             false,
	} {
		if err := CheckName(name); (err == nil) != ok {
			t.Errorf("CheckName(%.20q) = %v", name, err)
		}
	}
}
