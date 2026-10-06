package stackdeploy

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// Images enter a deploy as parameters of the run: the image of each server
// it rolls out, by digest (`--image shop-api=<repository>@sha256:...`). A
// server the run names no image for keeps the one the deploy manifest
// records. Building the images is not the deploy's job yet.
//
// A platform writes a server's image into the graph as its repository
// path, with no tag or digest (docs/stack-model.md, section 7.2), and the
// deploy pins it: every string property of the server's own nodes equal to
// the repository becomes `<repository>@<digest>`. The rendered program
// then names each image by digest, so a deploy rolls out exactly what it
// was given.

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ParseImages reads `--image` values, `<server>=<image>`, into a map from
// server to image. An image is `<repository>[:<tag>]@sha256:<digest>`;
// the tag is dropped, since the digest names the image.
func ParseImages(values []string) (map[string]string, error) {
	images := map[string]string{}
	for _, value := range values {
		server, image, ok := strings.Cut(value, "=")
		if !ok || server == "" || image == "" {
			return nil, fmt.Errorf("--image %q: want <server>=<repository>@sha256:<digest>", value)
		}
		repo, digest, err := SplitImage(image)
		if err != nil {
			return nil, fmt.Errorf("--image %s: %w", server, err)
		}
		if _, dup := images[server]; dup {
			return nil, fmt.Errorf("--image names server %s twice", server)
		}
		images[server] = repo + "@" + digest
	}
	return images, nil
}

// SplitImage splits an image reference by digest into its repository,
// without a tag, and its digest.
func SplitImage(image string) (repository, digest string, err error) {
	repository, digest, ok := strings.Cut(image, "@")
	if !ok || !digestPattern.MatchString(digest) {
		return "", "", fmt.Errorf("image %q is not pinned by digest: want <repository>@sha256:<64 hex digits>", image)
	}
	if slash := strings.LastIndex(repository, "/"); strings.Contains(repository[slash+1:], ":") {
		repository = repository[:slash+1+strings.Index(repository[slash+1:], ":")]
	}
	if repository == "" || strings.ContainsAny(repository, " \t\n") {
		return "", "", fmt.Errorf("image %q has no repository", image)
	}
	return repository, digest, nil
}

// checkImages refuses an image for a deployable env does not have or that
// is not a server, and returns the servers images names none for, sorted.
func checkImages(env *ir.ResolvedEnvironment, images map[string]string) (missing []string, err error) {
	var servers []string
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableServer {
			servers = append(servers, d.Name)
			if _, ok := images[d.Name]; !ok {
				missing = append(missing, d.Name)
			}
		}
	}
	for _, name := range sortedKeys(images) {
		if d := env.Deployable(name); d == nil || d.Kind != ir.DeployableServer {
			return nil, fmt.Errorf("--image names %s, which is not a server of environment %s (its servers: %s)", name, env.Environment, strings.Join(servers, ", "))
		}
	}
	return missing, nil
}

// PinImages returns a copy of env in which each server's image, by its
// repository, is pinned to the digest images names for it. It refuses an
// image whose repository no property of the server's own nodes holds.
func PinImages(env *ir.ResolvedEnvironment, images map[string]string) (*ir.ResolvedEnvironment, error) {
	pinned, err := copyEnvironment(env)
	if err != nil {
		return nil, err
	}
	for _, server := range sortedKeys(images) {
		image := images[server]
		repo, digest, err := SplitImage(image)
		if err != nil {
			return nil, fmt.Errorf("server %s: %w", server, err)
		}
		ref := repo + "@" + digest
		count := 0
		for _, res := range pinned.Resources.Resources {
			if res.Inherited || !slices.Contains(res.Owners, server) {
				continue
			}
			props, n := replaceString(map[string]any(res.Properties), repo, ref)
			res.Properties = props.(map[string]any)
			count += n
		}
		if count == 0 {
			return nil, fmt.Errorf("server %s: no property of its nodes holds the image repository %s; "+
				"its platform writes the server's image as a repository path, and --image gives that path with a digest", server, repo)
		}
	}
	return pinned, nil
}

// replaceString replaces every string in v equal to old with new, and
// returns how many it replaced.
func replaceString(v any, old, new string) (any, int) {
	switch v := v.(type) {
	case string:
		if v == old {
			return new, 1
		}
	case map[string]any:
		n := 0
		for key, inner := range v {
			replaced, count := replaceString(inner, old, new)
			v[key] = replaced
			n += count
		}
		return v, n
	case []any:
		n := 0
		for i, inner := range v {
			replaced, count := replaceString(inner, old, new)
			v[i] = replaced
			n += count
		}
		return v, n
	}
	return v, 0
}

// copyEnvironment returns a deep copy of env, through its JSON form.
func copyEnvironment(env *ir.ResolvedEnvironment) (*ir.ResolvedEnvironment, error) {
	data, err := stack.Marshal(env)
	if err != nil {
		return nil, err
	}
	return stack.Unmarshal(data)
}
