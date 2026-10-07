// Package release says which release of superschematic the running binary
// is part of, and what that release ships beside the binary for the code
// it generates: the static archives a generated Go server links through
// cgo, superscalar's and the version graph's (docs/stack-model.md, section
// 8.2; D47, amended).
//
// A binary built from a checkout is no release: its root module is at
// 0.0.0 or (devel), and Current's Version is empty. The release workflow
// builds the archives before the CLIs, and links each CLI with their
// digests (-X on archiveDigests), so a release binary pins what a
// generated Dockerfile and workflow download. A binary built any other
// way names no digests.
package release

import (
	"encoding/hex"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
)

const (
	// Module is superschematic's root module. Its version in the binary's
	// build information is the release, and its repository's release page
	// is where the release's assets download from.
	Module = "github.com/parable-work/superschematic"

	// ScalarGoModule is superscalar's Go binding. Its version in the
	// binary's build information is the one the binary links, the commit
	// superscalar.pin names, which the release's archives are built from.
	ScalarGoModule = "github.com/parable-work/superscalar/go"
)

// Platforms are the platforms a release ships the CLI and the archives
// for, by the names its assets carry.
var Platforms = []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64"}

// Release is a release of superschematic, as the code it generates pins
// it.
type Release struct {
	// Version is the release, without its `v`. Empty for a binary built
	// from a checkout.
	Version string

	// ScalarGo is the version of superscalar's Go binding the release
	// links, and builds the archives from.
	ScalarGo string

	// Archives are the hex SHA-256 digests of the release's archives
	// tarballs (ArchiveName), by platform. Empty for a binary the release
	// workflow did not build.
	Archives map[string]string
}

// archiveDigests is the release's archive digests, platform=digest pairs
// joined by commas, which the release workflow sets with -ldflags -X.
var archiveDigests string

// Current is the running binary's release, read from its build
// information and the digests the release workflow linked into it.
func Current() Release {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Release{}
	}
	r := Release{Version: versionOf(info, Module), ScalarGo: versionOf(info, ScalarGoModule)}
	if v := strings.TrimPrefix(r.Version, "v"); v == "" || v == "(devel)" || v == "0.0.0" {
		r.Version = ""
	} else {
		r.Version = v
	}
	if r.ScalarGo == "(devel)" {
		r.ScalarGo = ""
	}
	if digests, err := ParseDigests(archiveDigests); err == nil {
		r.Archives = digests
	}
	return r
}

// versionOf is module's version in the build information: the main
// module's, or a dependency's.
func versionOf(info *debug.BuildInfo, module string) string {
	if info.Main.Path == module {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == module {
			return dep.Version
		}
	}
	return ""
}

// ParseDigests reads platform=digest pairs joined by commas, as the
// release workflow writes them: every platform a known one, named once,
// and every digest a hex SHA-256.
func ParseDigests(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		platform, digest, ok := strings.Cut(pair, "=")
		switch {
		case !ok:
			return nil, fmt.Errorf("archive digest %q is not platform=digest", pair)
		case !slices.Contains(Platforms, platform):
			return nil, fmt.Errorf("archive digest for %q, which is no platform a release ships (%s)", platform, strings.Join(Platforms, ", "))
		case out[platform] != "":
			return nil, fmt.Errorf("two archive digests for %s", platform)
		}
		if b, err := hex.DecodeString(digest); err != nil || len(b) != 32 || strings.ToLower(digest) != digest {
			return nil, fmt.Errorf("archive digest for %s, %q, is not a lower-case hex SHA-256", platform, digest)
		}
		out[platform] = digest
	}
	return out, nil
}

// Platform names a GOOS and GOARCH pair as the release's assets do:
// linux and amd64 is linux-x64. It returns "" for a pair the release
// ships nothing for.
func Platform(goos, goarch string) string {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if arch == "" || (goos != "linux" && goos != "darwin") {
		return ""
	}
	return goos + "-" + arch
}

// ArchiveName is the file name of release version's archives tarball for
// platform. It holds one directory, named as the file without `.tar.gz`,
// whose lib/ holds libsuperscalar_ffi.a and
// libsuperschematic_versiongraph.a, built with one Rust release, so the
// two link into one binary.
func ArchiveName(version, platform string) string {
	return fmt.Sprintf("superschematic-archives_%s_%s.tar.gz", version, platform)
}

// DownloadURL is where release version's asset named name downloads from:
// the release page of Module's repository.
func DownloadURL(version, name string) string {
	return fmt.Sprintf("https://%s/releases/download/v%s/%s", Module, version, name)
}

// ModuleVersion is the version a go.mod requires one of the release's Go
// modules at: its tag, `v` and the release.
func (r Release) ModuleVersion() string {
	if r.Version == "" {
		return ""
	}
	return "v" + r.Version
}

// Archive returns the digest of the release's archives for platform.
func (r Release) Archive(platform string) (string, bool) {
	digest, ok := r.Archives[platform]
	return digest, ok && r.Version != ""
}
