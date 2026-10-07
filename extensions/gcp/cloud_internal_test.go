package gcp

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestTagResource names an image's tag in Artifact Registry.
func TestTagResource(t *testing.T) {
	got, err := tagResource("us-east1-docker.pkg.dev/acme-staging/shop/superschematic-migrate:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if want := "projects/acme-staging/locations/us-east1/repositories/shop/packages/superschematic-migrate/tags/1.2.3"; got != want {
		t.Errorf("tagResource = %s, want %s", got, want)
	}
	got, err = tagResource("europe-west1-docker.pkg.dev/p/r/a/b:context-ab")
	if err != nil || got != "projects/p/locations/europe-west1/repositories/r/packages/a%2Fb/tags/context-ab" {
		t.Errorf("a nested image: %s, %v", got, err)
	}
	for _, bad := range []string{
		"us-east1-docker.pkg.dev/acme-staging/shop/orders",
		"gcr.io/acme/orders:1",
		"us-east1-docker.pkg.dev/acme-staging/orders:1",
		"localhost:5000/orders",
	} {
		if _, err := tagResource(bad); err == nil {
			t.Errorf("tagResource(%s) is accepted", bad)
		}
	}
}

// TestLiteral puts a run's parameter values in a name.
func TestLiteral(t *testing.T) {
	params := map[string]string{"pr": "7"}
	got, err := literal(ir.Concat{"shop-api-pr", ir.Parameter("pr"), "@acme.iam"}, params)
	if err != nil || got != "shop-api-pr7@acme.iam" {
		t.Errorf("literal = %q, %v", got, err)
	}
	if _, err := literal(ir.Parameter("region"), params); err == nil || !strings.Contains(err.Error(), "region has no value") {
		t.Errorf("a parameter with no value: %v", err)
	}
	if _, err := literal(ir.Output{Resource: "x", Name: "name"}, params); err == nil {
		t.Error("an output is read as a string")
	}
}

// TestReleaseVersion: a test binary is built from a checkout, so it names
// no release.
func TestReleaseVersion(t *testing.T) {
	if v := releaseVersion(); v != "" {
		t.Errorf("releaseVersion = %q in a test binary", v)
	}
}
