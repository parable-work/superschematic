package stack

import (
	"fmt"
	"maps"
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// What a stack adds to its deployables' images (docs/stack-model.md,
// section 8.2, D144). A generated Dockerfile is written whole and never
// edited, so a workload that needs a program beside its runtime, a
// headless browser, a PDF tool or fonts, asks for it in the stack: `@stack`
// lists, per deployable that runs code, the Debian packages its image's
// final stage installs and a Dockerfile of the project's that extends the
// generated image. The stack names them once, since one image serves every
// environment: the stack's build writes them into the Dockerfile, and each
// environment's resolution records them on the deployable and checks that
// its platform builds an image at all.

// Images returns what in.Stack adds to the image of each deployable it
// names in `images`, by deployable, with the packages sorted. No
// environment changes it, and in.Environment is not read. The error is an
// *Errors with every failure found among the checks that decide it.
func Images(in Input) (map[string]*ir.ResolvedImage, error) {
	if in.Stack == nil {
		return nil, &Errors{List: []Error{{Code: CodeInvalidStack, Message: "no stack to read images from"}}}
	}
	in.Environment = ""
	r := newResolver(nil, in)
	r.indexServices()
	r.collectMembers()
	r.declareDeployables()
	r.defaultDeployables()
	r.defaultWorkers()
	r.defaultDerivers()
	r.defaultApps()
	r.resolveImages()
	if r.failed() {
		return nil, r.errs
	}
	images := map[string]*ir.ResolvedImage{}
	for name, d := range r.deployables {
		if d.res.Image != nil {
			images[name] = d.res.Image
		}
	}
	return images, nil
}

// resolveImages gives each deployable the stack's `images` name what the
// stack adds to its image (D144): its packages, sorted, and the Dockerfile
// that extends it. An element names a deployable that runs code, once.
func (r *resolver) resolveImages() {
	seen := map[string]int{}
	for i, image := range r.stack.Images {
		if image == nil {
			continue
		}
		where := fmt.Sprintf("stack %s images[%d] (of %s)", r.stack.Name, i, image.Of)
		d, ok := r.resolveDeployableRef(where, image.Of)
		if !ok {
			continue
		}
		if !d.res.Kind.RunsCode() {
			r.fail(CodeInvalidStack, "%s names %s %s, which runs no image; a server, a job, a worker, a deriver or an app has one", where, d.res.Kind, d.res.Name)
			continue
		}
		if first, dup := seen[d.res.Name]; dup {
			r.fail(CodeInvalidStack, "stack %s images[%d] and images[%d] both name %s %s; a deployable's image is named once", r.stack.Name, first, i, d.res.Kind, d.res.Name)
			continue
		}
		seen[d.res.Name] = i
		if err := registry.CheckDeployableImage(image); err != nil {
			r.fail(CodeInvalidStack, "%s %v", where, err)
			continue
		}
		packages := slices.Clone(image.Packages)
		slices.Sort(packages)
		d.res.Image = &ir.ResolvedImage{Packages: packages, Extend: image.Extend}
	}
}

// checkImages refuses, as unrealizable, what the stack adds to the image
// of a deployable whose platform builds none (D144): a bundle, as
// Cloudflare Workers run, has no system to install a package into and no
// Dockerfile to extend, and neither has an environment's one binary.
// Ignoring the setting would deploy a workload without the program it
// asked for.
func (r *resolver) checkImages() {
	for _, name := range slices.Sorted(maps.Keys(r.deployables)) {
		d := r.deployables[name]
		if d.res.Image == nil || d.res.Platform == "" || d.res.HasImage() {
			continue
		}
		r.fail(CodeUnrealizable, "%s %s: the stack adds to its image (%s), and platform %s builds no image for it but a %s; "+
			"leave it out of the stack's images, or place it in this environment on a platform that runs an image (D144)",
			d.res.Kind, name, describeImage(d.res.Image), d.res.Platform, d.res.Artifact)
	}
}

// describeImage says what a stack adds to an image, for an error or a
// notice: its packages and the file that extends it.
func describeImage(image *ir.ResolvedImage) string {
	switch {
	case len(image.Packages) > 0 && image.Extend != "":
		return fmt.Sprintf("packages %v and %s", image.Packages, image.Extend)
	case image.Extend != "":
		return image.Extend
	}
	return fmt.Sprintf("packages %v", image.Packages)
}
