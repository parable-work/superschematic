package topcoat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/registry"
)

// TestADependencyWithoutARustAPIIsWordedByWhy gives the reason a hosted
// reference is left out when registry.RustAPIOfService gives its API
// dependency no crate (D61, amended), by the dependency's outputs: no API
// server, a server that is not Rust, or, for a Rust server, a schema with
// no operations.
func TestADependencyWithoutARustAPIIsWordedByWhy(t *testing.T) {
	reg, err := registry.Assemble(registry.DefaultNaming(), Extension{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, tc := range []struct {
		name    string
		outputs map[string]any
		want    string
	}{
		{"no API server", map[string]any{"types": map[string]any{"rust": map[string]any{"enabled": true}}},
			"shelves-api's config generates no API server, so it has no crate"},
		{"an API server that is off", map[string]any{"api": map[string]any{"enabled": false, "language": "RUST"}},
			"shelves-api's config generates no API server, so it has no crate"},
		{"a Go server", map[string]any{"api": map[string]any{"enabled": true, "language": "GO"}, "types": map[string]any{"go": map[string]any{"enabled": true}}},
			"shelves-api's API server is not Rust, so it has no crate"},
		{"a Rust server", map[string]any{"api": map[string]any{"enabled": true, "language": "RUST"}, "types": map[string]any{"rust": map[string]any{"enabled": true}}},
			"shelves-api declares no operations, so its API crate has none to call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputs, err := registry.ParseOutputs(tc.outputs, reg)
			if err != nil {
				t.Fatalf("ParseOutputs: %v", err)
			}
			if got := noRustAPIOf("shelves-api", outputs); got != tc.want {
				t.Errorf("noRustAPIOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAHostedCratesIdentityFeaturesAreForwarded renders Cargo.toml for a
// crate whose API authenticates with the identity runtime (D50) and that
// calls two hosted crates (D61, amended), one whose API does too: its
// identity-* features turn on the API crate's and that hosted crate's, and
// not the other's, which declares none. A crate whose API does not
// authenticate so declares no features to forward.
func TestAHostedCratesIdentityFeaturesAreForwarded(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	c := &crate{
		SchemaName: "shop-api",
		CrateName:  "schemas-shop-api-topcoat",
		APICrate:   "schemas-shop-api",
		APIDepPath: "../../api/shop-api",
		Identity:   true,
		hosting: &hosting{crates: map[string]*hostedCrate{
			"shelves-api": {Service: "shelves-api", Crate: "schemas-shelves-api-topcoat", Path: "../shelves-api", Identity: true, used: true},
			"stock-api":   {Service: "stock-api", Crate: "schemas-stock-api-topcoat", Path: "../stock-api", used: true},
			"unused-api":  {Service: "unused-api", Crate: "schemas-unused-api-topcoat", Path: "../unused-api", Identity: true},
			"go-api":      nil,
		}},
	}
	render := func() string {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "cargo.tmpl", c); err != nil {
			t.Fatalf("render Cargo.toml: %v", err)
		}
		return buf.String()
	}
	manifest := render()
	for _, want := range []string{
		`identity-postgres = ["schemas-shop-api/identity-postgres", "schemas-shelves-api-topcoat/identity-postgres"]`,
		`identity-sqlite = ["schemas-shop-api/identity-sqlite", "schemas-shelves-api-topcoat/identity-sqlite"]`,
		`schemas-shelves-api-topcoat = { path = "../shelves-api" }`,
		`schemas-stock-api-topcoat = { path = "../stock-api" }`,
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("Cargo.toml lacks %q:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "schemas-stock-api-topcoat/identity") || strings.Contains(manifest, "unused-api") {
		t.Errorf("Cargo.toml forwards a feature no hosted crate declares, or depends on an unused one:\n%s", manifest)
	}
	c.Identity = false
	if manifest := render(); strings.Contains(manifest, "[features]") {
		t.Errorf("a crate without the identity runtime declares features:\n%s", manifest)
	}
}
