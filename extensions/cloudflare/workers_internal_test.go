package cloudflare

import (
	"encoding/json"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// TestDerivedBindings: each variable of a derived field is a plain_text
// binding, the variable of a secret member a secret_text binding that
// references its output, and a value that names a binding adds it: a D1
// database, a callee's Worker through a service binding, an R2 bucket
// (section 18.2). The connectors of D1, of calls between Workers and of R2
// derive these values; the platform renders whichever it is given.
func TestDerivedBindings(t *testing.T) {
	key := ir.Output{Resource: "edge.key", Name: "privateKeyPem"}
	ctx := registry.PlatformContext{
		Environment: registry.StackEnvironment{Stack: "edge-stack", Name: "Staging", Target: Target},
		Deployable: ir.ResolvedDeployable{Name: "shop-storefront", Kind: ir.DeployableServer, Bindings: []*ir.Binding{
			{Field: "SHOP_DB_DATABASE", Source: ir.BindingDerived, Value: map[string]any{
				"d1": map[string]any{"binding": "SHOP_DB_DATABASE", "database": ir.Output{Resource: "shop-db.database", Name: "uuid"}},
			}},
			{Field: "SHOP_PRICING_SERVICE", Source: ir.BindingDerived, Value: map[string]any{
				"url":        "https://shop-pricing",
				"binding":    map[string]any{"name": "SHOP_PRICING_SERVICE", "worker": "shop-pricing"},
				"credential": map[string]any{"source": "signed-token", "issuer": "shop-storefront", "audience": "shop-pricing", "key": key},
			}},
			{Field: "SHOP_MEDIA_BUCKET", Source: ir.BindingDerived, Value: map[string]any{
				"name":    "edge-stack-shop-media",
				"binding": "SHOP_MEDIA_BUCKET",
			}},
		}},
	}
	got, err := configBindings(ctx, accountValues{accountID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, b := range got {
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	want := []string{
		`{"id":{"$output":{"resource":"shop-db.database","name":"uuid"}},"name":"SHOP_DB_DATABASE","type":"d1"}`,
		`{"name":"SHOP_DB_DATABASE_D1_BINDING","text":"SHOP_DB_DATABASE","type":"plain_text"}`,
		`{"name":"SHOP_DB_DATABASE_D1_DATABASE","text":{"$output":{"resource":"shop-db.database","name":"uuid"}},"type":"plain_text"}`,
		`{"bucketName":"edge-stack-shop-media","name":"SHOP_MEDIA_BUCKET","type":"r2_bucket"}`,
		`{"name":"SHOP_MEDIA_BUCKET_BINDING","text":"SHOP_MEDIA_BUCKET","type":"plain_text"}`,
		`{"name":"SHOP_MEDIA_BUCKET_NAME","text":"edge-stack-shop-media","type":"plain_text"}`,
		`{"name":"SHOP_PRICING_SERVICE","service":"shop-pricing","type":"service"}`,
		`{"name":"SHOP_PRICING_SERVICE_BINDING_NAME","text":"SHOP_PRICING_SERVICE","type":"plain_text"}`,
		`{"name":"SHOP_PRICING_SERVICE_BINDING_WORKER","text":"shop-pricing","type":"plain_text"}`,
		`{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_AUDIENCE","text":"shop-pricing","type":"plain_text"}`,
		`{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_ISSUER","text":"shop-storefront","type":"plain_text"}`,
		`{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_KEY","text":{"$output":{"resource":"edge.key","name":"privateKeyPem"}},"type":"secret_text"}`,
		`{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_SOURCE","text":"signed-token","type":"plain_text"}`,
		`{"name":"SHOP_PRICING_SERVICE_URL","text":"https://shop-pricing","type":"plain_text"}`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("bindings are\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// TestResourceBindingRefusals: a value that names a binding without what
// the binding needs is refused, naming the field.
func TestResourceBindingRefusals(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{
		{map[string]any{"d1": map[string]any{"binding": "X"}}, "a D1 connection names its binding and its database"},
		{map[string]any{"binding": map[string]any{"worker": "shop-pricing"}}, "a service binding names itself and the callee's Worker"},
		{map[string]any{"binding": "X"}, "an R2 binding names itself and the bucket"},
		{map[string]any{"binding": 7.0}, "binding is float64"},
	} {
		if _, err := resourceBinding("F", tc.value); err == nil || !strings.Contains(err.Error(), "binding F: "+tc.want) {
			t.Errorf("%v: %v, want %q", tc.value, err, tc.want)
		}
	}
	for _, value := range []any{"postgres://x", map[string]any{"url": "postgres://x"}, nil} {
		if got, err := resourceBinding("F", value); got != nil || err != nil {
			t.Errorf("%v names a binding: %v, %v", value, got, err)
		}
	}
}

// TestTwoBindingsOfOneName: two bindings of one name are refused, as a
// derived binding named after another field would be.
func TestTwoBindingsOfOneName(t *testing.T) {
	ctx := registry.PlatformContext{
		Environment: registry.StackEnvironment{Stack: "edge-stack", Name: "Staging", Target: Target},
		Deployable: ir.ResolvedDeployable{Name: "shop-storefront", Kind: ir.DeployableServer, Bindings: []*ir.Binding{
			{Field: "MEDIA", Source: ir.BindingLiteral, Value: "x"},
			{Field: "SHOP_MEDIA_BUCKET", Source: ir.BindingDerived, Value: map[string]any{"name": "media", "binding": "MEDIA"}},
		}},
	}
	if _, err := configBindings(ctx, accountValues{}); err == nil || !strings.Contains(err.Error(), "server shop-storefront has two bindings named MEDIA") {
		t.Errorf("two bindings of one name: %v", err)
	}
}
