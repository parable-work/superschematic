package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/parable-work/superschematic/registry"
)

// Image builds (docs/stack-model.md, sections 7.2 and 11.2). The deploy
// hands the target each server's build context, a gzipped tarball of the
// repository root as the server's Dockerfile.dockerignore cuts it down,
// named by its digest. The builder uploads it to the state bucket and runs
// a Cloud Build build of the server's Dockerfile from it, as the stack's
// `builder` account, which pushes the image to the stack's Artifact
// Registry repository under the tag `context-<digest>`. A context whose
// tag already exists is not built again: the deploy that built it may have
// failed before it recorded the image, or `stack build` built it.

// buildTimeout bounds one build: a server's image compiles superscalar's
// Rust archive before its Go binary.
const buildTimeout = time.Hour

// buildPrefix is where the build contexts sit in the state bucket.
const buildPrefix = "superschematic/builds/"

// imageRepository is the Artifact Registry repository path of an image of
// the stack, in the repository bootstrap creates, named after the stack
// (section 7.3): `<region>-docker.pkg.dev/<project>/<stack>/<image>`. The
// Cloud Run platform writes a server's into the graph.
func imageRepository(v values, stack, image string) string {
	return v.region + "-docker.pkg.dev/" + v.project + "/" + kebab(stack) + "/" + kebab(image)
}

// accountEmail is the email of one of the stack's bootstrap accounts.
func accountEmail(v values, stack, role string) string {
	return accountID(stack, role) + "@" + v.project + ".iam.gserviceaccount.com"
}

// digestPattern is an image digest.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// imageBuilder is the gcp target's ImageBuilder.
type imageBuilder struct{ ext Extension }

var _ registry.ImageBuilder = imageBuilder{}

// Build builds the image of req's server from its context, or finds the
// one built from the same context, and returns it by digest.
func (b imageBuilder) Build(ctx context.Context, req registry.BuildRequest) (string, error) {
	if err := req.Check(); err != nil {
		return "", err
	}
	env := req.Run.Environment
	v, err := envValues(env)
	if err != nil {
		return "", err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	hex := strings.TrimPrefix(req.ContextDigest, "sha256:")
	repo := imageRepository(v, env.Stack, req.Server)
	object := buildPrefix + kebab(env.Stack) + "/" + kebab(req.Server) + "/" + hex + ".tar.gz"
	data, err := os.ReadFile(req.Context)
	if err != nil {
		return "", err
	}
	return b.ext.build(ctx, v, env.Stack, repo, "context-"+hex, req.Dockerfile, object, data, log)
}

// build runs a build of image repo:tag from a context's archive, which it
// uploads to object in the state bucket, unless the tag exists, and
// returns the image by digest.
func (e Extension) build(ctx context.Context, v values, stack, repo, tag, dockerfile, object string, archive []byte, log io.Writer) (string, error) {
	cloud := e.cloud()
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	image := repo + ":" + tag
	switch digest, err := cloud.ImageDigest(ctx, image); {
	case err == nil:
		logf("%s exists: %s", image, digest)
		return repo + "@" + digest, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	bucket := stateBucket(v.project)
	if err := cloud.WriteObject(ctx, bucket, object, archive); err != nil {
		return "", err
	}
	logf("build %s with Cloud Build from gs://%s/%s", image, bucket, object)
	result, err := cloud.RunBuild(ctx, v.project, v.region, BuildSpec{
		Bucket:         bucket,
		Object:         object,
		Dockerfile:     dockerfile,
		Image:          image,
		ServiceAccount: accountEmail(v, stack, "builder"),
		Timeout:        buildTimeout,
	})
	if err != nil {
		return "", err
	}
	if !digestPattern.MatchString(result.Digest) {
		return "", fmt.Errorf("gcp: build %s pushed %s with digest %q", result.ID, image, result.Digest)
	}
	logf("built %s: %s (logs: %s)", image, result.Digest, result.LogURL)
	return repo + "@" + result.Digest, nil
}
