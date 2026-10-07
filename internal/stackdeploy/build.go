package stackdeploy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A deploy builds the image of each server it is given no --image for,
// through its target's ImageBuilder, from the Dockerfile the stack's build
// wrote (docs/stack-model.md, sections 8.2 and 11.2). A server's image is
// built again only when its context changed: the deploy writes the
// context's archive, and when its digest is the one the manifest records
// for the image the server runs, the server keeps that image. A server
// whose language gets no Dockerfile yet, or whose Dockerfile the build has
// not written, keeps the manifest's image or takes one from --image.

// Sources says where a stack's build wrote each server's Dockerfile, and
// the directory every image builds from.
type Sources struct {
	// OutputRoot is where the stack's build wrote: a Go server's
	// Dockerfile is at server/<stack>/<server>/Dockerfile under it.
	OutputRoot string

	// RepositoryRoot is every image's build context: the repository root,
	// the parent of the schemas root, or the naming file's [paths]
	// build_context.
	RepositoryRoot string
}

// Dockerfile returns the path of a server's Dockerfile.
func (s Sources) Dockerfile(stack, server string) string {
	return filepath.Join(servergen.ServerDir(s.OutputRoot, stack, server), servergen.DockerFile)
}

// serverBuild is one image a deploy or a build builds.
type serverBuild struct {
	server  string
	archive string
	context *Context
}

// imagePlan is where each server's image comes from in one deploy or
// build: those decided before any build, by --image or the manifest, and
// those to build.
type imagePlan struct {
	// images and contexts hold, by server, each image decided so far, and
	// the digest of the context each was built from when a deploy built
	// it; a build fills both in.
	images   map[string]string
	contexts map[string]string

	// builds are the images to build, in server order; unchanged are the
	// servers whose context is the one their image was built from.
	builds    []*serverBuild
	unchanged []string
	built     []string

	dir string
}

// cleanup removes the archives the plan wrote.
func (p *imagePlan) cleanup() {
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// wanted returns every server the plan gives an image, decided or to be
// built, with the decided image or "" for one to build.
func (p *imagePlan) wanted() map[string]string {
	out := maps.Clone(p.images)
	for _, b := range p.builds {
		out[b.server] = ""
	}
	return out
}

// planImages decides where each server's image comes from: given, the
// images --image names, first; then, when src is set and the target
// builds images, a build of each server in only (every server when only is
// empty) whose Dockerfile exists and whose context changed since the
// image the manifest records, or every such server when force is set;
// then the manifest's image.
func (s *session) planImages(prev *Manifest, given map[string]string, src *Sources, only []string, force bool) (*imagePlan, error) {
	p := &imagePlan{images: map[string]string{}, contexts: map[string]string{}}
	if prev != nil {
		for server, image := range prev.Images {
			if d := s.env.Deployable(server); d != nil && d.Kind == ir.DeployableServer {
				p.images[server] = image
				if c := prev.Contexts[server]; c != "" {
					p.contexts[server] = c
				}
			}
		}
	}
	for server, image := range given {
		p.images[server] = image
		delete(p.contexts, server)
	}
	if src == nil || s.target.Builder == nil {
		return p, nil
	}
	for _, d := range s.env.Deployables {
		if d.Kind != ir.DeployableServer {
			continue
		}
		if _, ok := given[d.Name]; ok {
			continue
		}
		if len(only) > 0 && !slices.Contains(only, d.Name) {
			continue
		}
		dockerfile := src.Dockerfile(s.env.Stack, d.Name)
		if _, err := os.Stat(dockerfile); errors.Is(err, fs.ErrNotExist) {
			if len(only) > 0 {
				return p, fmt.Errorf("server %s has no Dockerfile at %s: run `superschematic build-all` first, or give its image with --image", d.Name, dockerfile)
			}
			s.logf("server %s: no Dockerfile at %s, so its image comes from --image or the manifest", d.Name, dockerfile)
			continue
		} else if err != nil {
			return p, err
		}
		if p.dir == "" {
			dir, err := os.MkdirTemp("", "superschematic-build-")
			if err != nil {
				return p, err
			}
			p.dir = dir
		}
		archive := filepath.Join(p.dir, d.Name+".tar.gz")
		c, err := writeContextFile(archive, src.RepositoryRoot, dockerfile)
		if err != nil {
			return p, fmt.Errorf("server %s: %w", d.Name, err)
		}
		if !force && p.images[d.Name] != "" && p.contexts[d.Name] == c.Digest {
			s.logf("server %s: context %s is the one %s was built from; keeping it", d.Name, shortDigest(c.Digest), p.images[d.Name])
			p.unchanged = append(p.unchanged, d.Name)
			continue
		}
		s.logf("server %s: context %s, %d files, %s", d.Name, shortDigest(c.Digest), c.Files, byteSize(c.Size))
		p.builds = append(p.builds, &serverBuild{server: d.Name, archive: archive, context: c})
	}
	return p, nil
}

// writeContextFile writes a build context's archive to path.
func writeContextFile(path, root, dockerfile string) (*Context, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	c, err := WriteContext(f, root, dockerfile)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return c, err
}

// runBuilds builds each image the plan holds through the target's
// builder, and records each in the plan.
func (s *session) runBuilds(ctx context.Context, p *imagePlan) error {
	for _, b := range p.builds {
		req := registry.BuildRequest{
			Run:           s.run,
			Server:        b.server,
			Context:       b.archive,
			ContextDigest: b.context.Digest,
			Dockerfile:    b.context.Dockerfile,
			Log:           s.log,
		}
		if err := req.Check(); err != nil {
			return err
		}
		s.logf("build %s from %s", b.server, b.context.Dockerfile)
		image, err := s.target.Builder.Build(ctx, req)
		if err != nil {
			return fmt.Errorf("build the image of %s: %w", b.server, err)
		}
		repo, digest, err := SplitImage(image)
		if err != nil {
			return fmt.Errorf("the image target %s built for %s: %w", s.target.Name, b.server, err)
		}
		image = repo + "@" + digest
		s.logf("built %s: %s", b.server, image)
		p.images[b.server] = image
		p.contexts[b.server] = b.context.Digest
		p.built = append(p.built, b.server)
	}
	return nil
}

func shortDigest(digest string) string {
	if len(digest) > len("sha256:")+12 {
		return digest[:len("sha256:")+12]
	}
	return digest
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// BuildOptions is one `stack build`.
type BuildOptions struct {
	Options

	// Sources says where the stack's build wrote each Dockerfile.
	Sources Sources

	// Servers names the servers to build; empty builds every server that
	// has a Dockerfile.
	Servers []string

	// Force builds a server whose context is the one the image the
	// manifest records was built from.
	Force bool
}

// BuildResult is what a build made.
type BuildResult struct {
	// Run names the run.
	Run string `json:"run"`

	// Images holds the image of each server built or unchanged, by
	// server: what `stack deploy --image` takes.
	Images map[string]string `json:"images"`

	// Built are the servers built; Unchanged those whose context is the
	// one their deployed image was built from.
	Built     []string `json:"built,omitempty"`
	Unchanged []string `json:"unchanged,omitempty"`
}

// Build builds the images of a run's servers without deploying them
// (docs/stack-model.md, section 11.1): each server's whose context changed
// since the image the manifest records, as a deploy would build it. It
// writes no manifest, so a deploy takes the images with --image; the
// target's builder may also find an image it built for the same context
// without building it again.
func Build(ctx context.Context, o BuildOptions) (*BuildResult, error) {
	s, err := open(o.Options)
	if err != nil {
		return nil, err
	}
	if s.target.Builder == nil {
		return nil, fmt.Errorf("target %s builds no images; give each server's image with --image", s.target.Name)
	}
	if s.target.State == nil {
		return nil, fmt.Errorf("target %s keeps no deploy state, so environment %s does not deploy", s.target.Name, s.env.Environment)
	}
	for _, server := range o.Servers {
		if d := s.env.Deployable(server); d == nil || d.Kind != ir.DeployableServer {
			return nil, fmt.Errorf("environment %s has no server %s", s.env.Environment, server)
		}
	}
	prev, err := readManifest(ctx, s.target.State, s.run)
	if err != nil {
		return nil, err
	}
	src := o.Sources
	p, err := s.planImages(prev, nil, &src, o.Servers, o.Force)
	defer p.cleanup()
	if err != nil {
		return nil, err
	}
	if err := s.runBuilds(ctx, p); err != nil {
		return nil, err
	}
	out := &BuildResult{Run: s.run.Name(), Images: map[string]string{}, Built: p.built, Unchanged: p.unchanged}
	for _, server := range append(slices.Clone(p.built), p.unchanged...) {
		out.Images[server] = p.images[server]
	}
	slices.Sort(out.Built)
	slices.Sort(out.Unchanged)
	return out, nil
}

// ImageFlags returns the images as `--image` values, in server order.
func (r *BuildResult) ImageFlags() []string {
	var out []string
	for _, server := range sortedKeys(r.Images) {
		out = append(out, server+"="+r.Images[server])
	}
	return out
}

// describeMissing explains why servers have no image.
func describeMissing(missing []string, builds bool) string {
	if builds {
		return fmt.Sprintf("no image for server %s: the manifest records none and there is no Dockerfile to build one from; "+
			"run `superschematic build-all`, or pass --image <server>=<repository>@sha256:<digest> for each", strings.Join(missing, ", "))
	}
	return fmt.Sprintf("no image for server %s: the manifest records none, so pass --image <server>=<repository>@sha256:<digest> for each", strings.Join(missing, ", "))
}
