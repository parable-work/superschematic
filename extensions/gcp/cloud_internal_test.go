package gcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"

	ir "github.com/parable-work/superschematic/ir"
)

// TestBuildEnded: waitBuild stops at every status but the four of a build
// that has yet to end. A status Cloud Build adds fails here until it is
// placed.
func TestBuildEnded(t *testing.T) {
	running := map[cloudbuildpb.Build_Status]bool{
		cloudbuildpb.Build_STATUS_UNKNOWN: true,
		cloudbuildpb.Build_PENDING:        true,
		cloudbuildpb.Build_QUEUED:         true,
		cloudbuildpb.Build_WORKING:        true,
	}
	ended := map[cloudbuildpb.Build_Status]bool{
		cloudbuildpb.Build_SUCCESS:        true,
		cloudbuildpb.Build_FAILURE:        true,
		cloudbuildpb.Build_INTERNAL_ERROR: true,
		cloudbuildpb.Build_TIMEOUT:        true,
		cloudbuildpb.Build_CANCELLED:      true,
		cloudbuildpb.Build_EXPIRED:        true,
	}
	for value, name := range cloudbuildpb.Build_Status_name {
		status := cloudbuildpb.Build_Status(value)
		if !running[status] && !ended[status] {
			t.Errorf("status %s is neither running nor ended", name)
		}
		if got := buildEnded(status); got != ended[status] {
			t.Errorf("buildEnded(%s) = %v", name, got)
		}
	}
}

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

// TestUntilEnabled: bootstrap retries a call that an API it has just
// enabled refuses, in the words the KMS client and the provisioner used on
// a fresh project, and returns any other error, or the refusal once the
// propagation window has passed, at once.
func TestUntilEnabled(t *testing.T) {
	defer func(p, r time.Duration) { apiPropagation, apiRetry = p, r }(apiPropagation, apiRetry)
	apiPropagation, apiRetry = time.Second, time.Millisecond
	ctx := context.Background()
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	for _, refusal := range []string{
		"gcp: key ring projects/p/locations/r/keyRings/k: rpc error: code = PermissionDenied desc = Google Cloud KMS API has not been used in project 100000 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/cloudkms.googleapis.com/overview?project=100000 then retry.",
		"pulumi: up p, step infrastructure: googleapi: Error 403: Google Cloud KMS API has not been used in project 100000 before or it is disabled., forbidden",
		"rpc error: code = PermissionDenied desc = reason: SERVICE_DISABLED",
	} {
		calls := 0
		err := untilEnabled(ctx, logf, "the state key", func() error {
			if calls++; calls < 3 {
				return errors.New(refusal)
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Errorf("%q: err %v after %d calls, want success on the third", refusal, err, calls)
		}
	}
	if len(logged) != 6 || !strings.Contains(logged[0], "the state key") {
		t.Errorf("logged %q, want a line per retry naming the call", logged)
	}

	calls := 0
	other := errors.New("rpc error: code = PermissionDenied desc = the caller does not have permission")
	if err := untilEnabled(ctx, logf, "x", func() error { calls++; return other }); err != other || calls != 1 {
		t.Errorf("another error: %v after %d calls, want it at once", err, calls)
	}

	apiPropagation = 0
	calls = 0
	disabled := errors.New("the API has not been used in project 100000 before or it is disabled")
	if err := untilEnabled(ctx, logf, "x", func() error { calls++; return disabled }); err != disabled || calls != 1 {
		t.Errorf("past the window: %v after %d calls, want the refusal at once", err, calls)
	}
}
