package registry

import (
	"fmt"
	"slices"
	"strings"
)

// A bucket's CORS rules (D148). A browser that uploads to a signed URL, or
// reads one from a page of another origin, sends the bucket a preflight
// the bucket must answer, so a bucket's settings in an environment take
// `cors`, a list of rules. The setting is the same on every platform that
// keeps a bucket: each puts BucketCORSSchema in its settings schema and
// reads the checked rules with BucketCORS, then writes them in its
// provider's shape.

// BucketCORSRule is one rule of a bucket's `cors` setting: the origins it
// answers, the methods it admits from them, the request headers a
// preflight may name, the response headers a page may read, and how long a
// browser may keep the preflight's answer.
type BucketCORSRule struct {
	// Origins are the origins the rule answers, each a scheme and a host
	// (`https://shop.acme.dev`), or `*` for any origin.
	Origins []string

	// Methods are the HTTP methods the rule admits, in upper case.
	Methods []string

	// Headers are the request headers a preflight may name.
	Headers []string

	// ExposeHeaders are the response headers a page may read beside the
	// ones every browser exposes.
	ExposeHeaders []string

	// MaxAgeSeconds is how long a browser may keep a preflight's answer;
	// zero says nothing, and the browser's own default applies.
	MaxAgeSeconds int
}

// BucketCORSSchema is the JSON Schema of the `cors` setting, which a
// bucket platform's settings schema holds under `cors`. maxAge is a
// duration of whole seconds as Go writes one (`10m`, `1h`).
const BucketCORSSchema = `{
  "type": "array",
  "minItems": 1,
  "items": {
    "type": "object",
    "required": ["origins", "methods"],
    "properties": {
      "origins": {"type": "array", "minItems": 1, "items": {"type": "string", "pattern": "^(\\*|https?://[^/?#\\s*]+)$"}},
      "methods": {"type": "array", "minItems": 1, "items": {"enum": ["GET", "HEAD", "PUT", "POST", "DELETE"]}},
      "headers": {"type": "array", "items": {"type": "string", "pattern": "^(\\*|[A-Za-z0-9!#$%&'*+.^_|~-]+)$"}},
      "exposeHeaders": {"type": "array", "items": {"type": "string", "pattern": "^[A-Za-z0-9!#$%&'*+.^_|~-]+$"}},
      "maxAge": {"type": "string", "minLength": 1}
    },
    "additionalProperties": false
  }
}`

// BucketCORSMaxAgeSeconds is the longest a rule's maxAge may be, a day:
// what R2 takes, and more than any browser keeps a preflight.
const BucketCORSMaxAgeSeconds = 24 * 60 * 60

// BucketCORS reads the `cors` setting of a bucket's settings, which the
// platform's schema already held to BucketCORSSchema, and checks what a
// schema cannot say. It returns no rules when the settings name none.
//
// A rule that admits PUT from any origin is refused: every Bucket
// service's bucket is private, and a page writes to it through a URL an
// API signed, so the rule names the origins of the pages that upload. A
// rule may still answer GET and HEAD for `*`, as a signed download any
// page may fetch.
func BucketCORS(bucket string, settings map[string]any) ([]BucketCORSRule, error) {
	raw, ok := settings["cors"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("the cors setting of bucket %s is no list of rules", bucket)
	}
	rules := make([]BucketCORSRule, 0, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cors rule %d of bucket %s is no object", i+1, bucket)
		}
		rule := BucketCORSRule{
			Origins:       corsStrings(obj["origins"]),
			Methods:       corsStrings(obj["methods"]),
			Headers:       corsStrings(obj["headers"]),
			ExposeHeaders: corsStrings(obj["exposeHeaders"]),
		}
		if len(rule.Origins) == 0 || len(rule.Methods) == 0 {
			return nil, fmt.Errorf("cors rule %d of bucket %s names no origins or no methods; a rule names both", i+1, bucket)
		}
		if slices.Contains(rule.Origins, "*") {
			if len(rule.Origins) > 1 {
				return nil, fmt.Errorf("cors rule %d of bucket %s names the origin * beside others (%s); * is every origin, so name it alone or name the origins", i+1, bucket, strings.Join(rule.Origins, ", "))
			}
			if slices.Contains(rule.Methods, "PUT") {
				return nil, fmt.Errorf("cors rule %d of bucket %s admits PUT from the origin *; the bucket is private and a page uploads to it through a signed URL, so name the origins of the pages that upload, as origins: [\"https://shop.acme.dev\"] (D148)", i+1, bucket)
			}
		}
		if maxAge, ok := obj["maxAge"].(string); ok && maxAge != "" {
			seconds, err := TimeoutSeconds(maxAge)
			if err != nil {
				return nil, fmt.Errorf("cors rule %d of bucket %s: maxAge: %w", i+1, bucket, err)
			}
			if seconds > BucketCORSMaxAgeSeconds {
				return nil, fmt.Errorf("cors rule %d of bucket %s: maxAge is %q, over a day; a browser keeps a preflight's answer two hours at most, so give it a day or less", i+1, bucket, maxAge)
			}
			rule.MaxAgeSeconds = seconds
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// corsStrings reads a list of strings a schema checked.
func corsStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
