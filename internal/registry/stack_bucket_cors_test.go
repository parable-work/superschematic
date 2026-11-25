package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// TestBucketCORS reads a bucket's `cors` setting (D148): the rules as
// written, a maxAge in seconds, and no rules for settings that name none.
func TestBucketCORS(t *testing.T) {
	if rules, err := BucketCORS("shop-media", map[string]any{"versioning": true}); err != nil || rules != nil {
		t.Errorf("settings with no cors read as %v, %v; want no rules", rules, err)
	}
	rules, err := BucketCORS("shop-media", map[string]any{"cors": []any{
		map[string]any{
			"origins": []any{"https://shop.acme.dev", "http://localhost:5173"}, "methods": []any{"GET", "PUT"},
			"headers": []any{"content-type", "x-goog-meta-owner"}, "exposeHeaders": []any{"etag"}, "maxAge": "10m",
		},
		map[string]any{"origins": []any{"*"}, "methods": []any{"GET", "HEAD"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []BucketCORSRule{
		{
			Origins: []string{"https://shop.acme.dev", "http://localhost:5173"}, Methods: []string{"GET", "PUT"},
			Headers: []string{"content-type", "x-goog-meta-owner"}, ExposeHeaders: []string{"etag"}, MaxAgeSeconds: 600,
		},
		{Origins: []string{"*"}, Methods: []string{"GET", "HEAD"}, Headers: []string{}, ExposeHeaders: []string{}},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("the rules are %+v, want %+v", rules, want)
	}
}

// TestBucketCORSRefusals: a rule that admits PUT from any origin is
// refused, since the bucket is private and a page uploads through a signed
// URL, and so are `*` beside named origins and a maxAge that is no
// duration of whole seconds, or over a day.
func TestBucketCORSRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		rule map[string]any
		want string
	}{
		"PUT from any origin": {
			map[string]any{"origins": []any{"*"}, "methods": []any{"GET", "PUT"}},
			"cors rule 1 of bucket shop-media admits PUT from the origin *; the bucket is private and a page uploads to it through a signed URL, so name the origins of the pages that upload",
		},
		"* beside an origin": {
			map[string]any{"origins": []any{"*", "https://shop.acme.dev"}, "methods": []any{"GET"}},
			"names the origin * beside others (*, https://shop.acme.dev)",
		},
		"a maxAge that is no duration": {
			map[string]any{"origins": []any{"https://shop.acme.dev"}, "methods": []any{"GET"}, "maxAge": "an hour"},
			`maxAge: "an hour" is no duration`,
		},
		"a maxAge of a fraction of a second": {
			map[string]any{"origins": []any{"https://shop.acme.dev"}, "methods": []any{"GET"}, "maxAge": "1500ms"},
			`maxAge: "1500ms" is not a whole number of seconds`,
		},
		"a maxAge over a day": {
			map[string]any{"origins": []any{"https://shop.acme.dev"}, "methods": []any{"GET"}, "maxAge": "25h"},
			`maxAge is "25h", over a day`,
		},
		"no origins": {
			map[string]any{"methods": []any{"GET"}},
			"names no origins or no methods",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BucketCORS("shop-media", map[string]any{"cors": []any{c.rule}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one saying %q", err, c.want)
			}
		})
	}
}

// TestBucketCORSSchema holds the setting's schema to what a platform's
// settings schema checks before BucketCORS reads the rules: origins that
// are a scheme and a host or `*`, the methods a bucket answers, header
// names, and no other key.
func TestBucketCORSSchema(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(BucketCORSSchema), &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("cors.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("cors.json")
	if err != nil {
		t.Fatal(err)
	}
	rule := func(over map[string]any) []any {
		r := map[string]any{"origins": []any{"https://shop.acme.dev"}, "methods": []any{"PUT"}}
		for k, v := range over {
			if v == nil {
				delete(r, k)
			} else {
				r[k] = v
			}
		}
		return []any{r}
	}
	for name, c := range map[string]struct {
		value any
		ok    bool
	}{
		"a rule":                    {rule(nil), true},
		"every key":                 {rule(map[string]any{"headers": []any{"content-type", "*"}, "exposeHeaders": []any{"etag"}, "maxAge": "1h"}), true},
		"any origin":                {rule(map[string]any{"origins": []any{"*"}}), true},
		"a local origin":            {rule(map[string]any{"origins": []any{"http://localhost:5173"}}), true},
		"no rules":                  {[]any{}, false},
		"no methods":                {rule(map[string]any{"methods": nil}), false},
		"no origins":                {rule(map[string]any{"origins": []any{}}), false},
		"an origin with a path":     {rule(map[string]any{"origins": []any{"https://shop.acme.dev/upload"}}), false},
		"an origin with a wildcard": {rule(map[string]any{"origins": []any{"https://*.acme.dev"}}), false},
		"an origin with no scheme":  {rule(map[string]any{"origins": []any{"shop.acme.dev"}}), false},
		"OPTIONS":                   {rule(map[string]any{"methods": []any{"OPTIONS"}}), false},
		"a lower-case method":       {rule(map[string]any{"methods": []any{"put"}}), false},
		"a header with a space":     {rule(map[string]any{"headers": []any{"content type"}}), false},
		"an unknown key":            {rule(map[string]any{"credentials": true}), false},
		"a maxAge in seconds":       {rule(map[string]any{"maxAge": float64(3600)}), false},
	} {
		t.Run(name, func(t *testing.T) {
			err := schema.Validate(c.value)
			if (err == nil) != c.ok {
				t.Errorf("validation says %v, want ok %v", err, c.ok)
			}
		})
	}
}
