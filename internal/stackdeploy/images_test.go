package stackdeploy_test

import (
	"encoding/json"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

// TestPinImagesPinsAPartOfAConcatenation (D67, amended): the deploy pins
// the image where a node's property holds its repository whole, and where
// a concatenation holds it as a part of its own, as the gcp target writes
// it into OTEL_RESOURCE_ATTRIBUTES; a repository that only begins a part,
// as another deployable's does, stays.
func TestPinImagesPinsAPartOfAConcatenation(t *testing.T) {
	repo := "us-east1-docker.pkg.dev/acme/shop/shop-api"
	digest := "sha256:" + "ab0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"[:64]
	env := &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Environment: "Staging",
		Resources: &ir.ResourceGraph{Resources: []*ir.Resource{{
			ID:     "shop-api.service",
			Type:   "gcp:cloudrunv2/service:Service",
			Owners: []string{"shop-api"},
			Properties: map[string]any{
				"image": repo,
				"envs": []any{
					map[string]any{"name": "OTEL_RESOURCE_ATTRIBUTES", "value": ir.Concat{"service.version=", repo}},
					map[string]any{"name": "OTHER", "value": ir.Concat{"x=", repo + "-ship-orders"}},
				},
			},
		}}},
	}
	pinned, err := stackdeploy.PinImages(env, map[string]string{"shop-api": repo + "@" + digest})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(pinned.Resources.Resources[0].Properties)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"envs":[{"name":"OTEL_RESOURCE_ATTRIBUTES","value":{"$concat":["service.version=","` + repo + "@" + digest + `"]}},` +
		`{"name":"OTHER","value":{"$concat":["x=","` + repo + `-ship-orders"]}}],"image":"` + repo + "@" + digest + `"}`
	if string(got) != want {
		t.Errorf("the pinned properties are\n%s\nwant\n%s", got, want)
	}
}
