package servergen

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// What a stack adds to an entrypoint's image (docs/stack-model.md, section
// 8.2, D144). A generated Dockerfile is written whole and never edited, so
// a workload that needs a program beside its runtime asks for it in the
// stack, per deployable: Debian packages, which the Dockerfile's final
// stage installs, and a Dockerfile of the project's that extends the image
// the generated one builds.

// ImageInput is what the stack adds to the image of the entrypoint a plan
// is for.
type ImageInput struct {
	// Packages are the Debian packages the final stage installs.
	Packages []string

	// Extend is the Dockerfile that extends the generated image, as the
	// stack names it, slash-separated and relative to the repository root,
	// and ExtendFile its path on disk. Both are empty when the stack names
	// none.
	Extend     string
	ExtendFile string
}

// Image is the part of a Dockerfile's plan the stack adds.
type Image struct {
	// Packages are the Debian packages the final stage installs, sorted,
	// each once.
	Packages []string

	// Extend is the path, in the build context, of the Dockerfile that
	// extends the image this one builds, or empty. A build of it takes
	// the build argument registry.ImageBaseArg, which names that image.
	Extend string
}

// debianBasePackages are what a Go or Rust image's final stage installs
// beside the stack's packages. Such an image runs its binary on distroless
// cc, which has no apt-get to install a package with, so when the stack
// adds packages its Dockerfile's final stage starts from the slim image of
// the Debian release the build stage links the binary against, trixie,
// which distroless cc is built from too. The slim image lacks what
// distroless holds beside glibc and libgcc and a binary that ran there
// reads: the CA certificates a TLS client trusts, OpenSSL, which a Rust
// binary links, and the time zones.
var debianBasePackages = []string{"ca-certificates", "openssl", "tzdata"}

// Install returns the packages the final stage of a Go or Rust image
// installs: the stack's, and what distroless held, sorted, each once.
func (i *Image) Install() []string {
	out := append(slices.Clone(debianBasePackages), i.Packages...)
	slices.Sort(out)
	return slices.Compact(out)
}

// planImage plans what the stack adds to the image of the entrypoint name,
// a server or a job say (kind), whose Dockerfile's build context is root
// and holds include. It returns the plan, nil when the stack adds nothing,
// and the paths the context then holds: include, and with them the
// Dockerfile that extends the image and, unless that is the context's
// root, the directory that holds it, so the file copies what lies beside
// it. It refuses a Dockerfile that is no file, or lies outside the
// context.
func planImage(in *ImageInput, kind, name, root string, include []string) (*Image, []string, error) {
	if in == nil || (len(in.Packages) == 0 && in.Extend == "") {
		return nil, include, nil
	}
	image := &Image{Packages: slices.Clone(in.Packages)}
	slices.Sort(image.Packages)
	image.Packages = slices.Compact(image.Packages)
	if in.Extend == "" {
		return image, include, nil
	}
	info, err := os.Stat(in.ExtendFile)
	switch {
	case err != nil:
		return nil, nil, fmt.Errorf("%s %s: the stack extends its image with %s, which is no file at %s: write the Dockerfile there, or leave extend out of the stack's images (D144)", kind, name, in.Extend, in.ExtendFile)
	case !info.Mode().IsRegular():
		return nil, nil, fmt.Errorf("%s %s: the stack extends its image with %s, and %s is not a file (D144)", kind, name, in.Extend, in.ExtendFile)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	file, err := filepath.Abs(in.ExtendFile)
	if err != nil {
		return nil, nil, err
	}
	r, err := filepath.Rel(abs, file)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return nil, nil, fmt.Errorf("%s %s: the stack extends its image with %s, and %s lies outside the repository root %s, the build context; move it under the context (D144)", kind, name, in.Extend, in.ExtendFile, abs)
	}
	image.Extend = filepath.ToSlash(r)
	include = append(slices.Clone(include), image.Extend)
	if dir := path.Dir(image.Extend); dir != "." {
		include = append(include, dir)
	}
	slices.Sort(include)
	return image, slices.Compact(include), nil
}
