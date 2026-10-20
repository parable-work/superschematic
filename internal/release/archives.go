package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A release's static archives on the machine that runs superschematic
// (D47, amended). A Go build of the code a release generates, in a project
// whose naming file names no superscalar checkout, links superscalar's
// archive, the version graph's and the engine's through cgo, and no module
// the module proxy serves carries them. `stack dev` and `stack run` fetch
// the release's tarball for the machine's platform, as the generated CI's
// check job does: they download it, check it against the digest the
// binary names, keep it in a cache, and point cgo's linker at it with
// CGO_LDFLAGS. `superschematic env` prints the same CGO_LDFLAGS for a
// `go build` run by hand.

// MirrorEnv names the variable that holds the base URL a release's assets
// download from in place of its release page: a mirror, or a stand-in in
// tests. A server's Dockerfile takes a build argument of the same name.
const MirrorEnv = "SUPERSCHEMATIC_RELEASE"

// CGOLinkerFlagsEnv is the variable cgo reads the linker's flags from.
const CGOLinkerFlagsEnv = "CGO_LDFLAGS"

// ArchivesCacheDir is where FetchArchives keeps the archives it fetched:
// superschematic/archives in the user's cache directory (os.UserCacheDir).
func ArchivesCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("the cache directory the static archives are kept in: %w", err)
	}
	return filepath.Join(dir, "superschematic", "archives"), nil
}

// ArchivesBase is the base URL a release's assets download from: MirrorEnv
// when it is set, else the release's page (DownloadURL).
func (r Release) ArchivesBase() string {
	if base := strings.TrimSpace(os.Getenv(MirrorEnv)); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	return strings.TrimSuffix(DownloadURL(r.Version, ""), "/")
}

// FetchArchives returns the directory that holds the release's static
// archives for platform, whose lib/ cgo's linker reads: a directory of
// cache named for the tarball's digest, which it downloads from base when
// the cache lacks it. It refuses a release the release workflow did not
// build, which names no digest, a tarball whose SHA-256 is not the digest,
// and an entry of the tarball that would land outside the directory. A
// tarball is unpacked whole into a directory of its own beside the one it
// is named for, then renamed to it, so the directory is either missing or
// complete, and two fetches at once leave one.
func (r Release) FetchArchives(ctx context.Context, platform, cache, base string) (string, error) {
	digest, ok := r.Archive(platform)
	if !ok {
		if r.Version == "" {
			return "", errors.New("this superschematic is built from a checkout, which is no release, and ships no static archives")
		}
		return "", fmt.Errorf("superschematic %s, which its release workflow did not build, names no digest of its static archives for %s", r.Version, platform)
	}
	dir := filepath.Join(cache, digest)
	if info, err := os.Stat(filepath.Join(dir, "lib")); err == nil && info.IsDir() {
		return dir, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", fmt.Errorf("static archives: %w", err)
	}
	name := ArchiveName(r.Version, platform)
	url := strings.TrimSuffix(base, "/") + "/" + name
	tarball, err := os.CreateTemp(cache, "download-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("static archives: %w", err)
	}
	defer func() { _ = os.Remove(tarball.Name()) }()
	defer func() { _ = tarball.Close() }()
	sum, err := download(ctx, url, tarball)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if sum != digest {
		return "", fmt.Errorf("%s has SHA-256 %s, and superschematic %s names %s for it; it is not the release's, so nothing of it is kept", url, sum, r.Version, digest)
	}
	unpacked, err := os.MkdirTemp(cache, "unpack-")
	if err != nil {
		return "", fmt.Errorf("static archives: %w", err)
	}
	defer func() { _ = os.RemoveAll(unpacked) }()
	if _, err := tarball.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("static archives: %w", err)
	}
	if err := unpack(tarball, unpacked); err != nil {
		return "", fmt.Errorf("unpack %s: %w", name, err)
	}
	if info, err := os.Stat(filepath.Join(unpacked, "lib")); err != nil || !info.IsDir() {
		return "", fmt.Errorf("unpack %s: it holds no lib/ directory", name)
	}
	if err := os.Rename(unpacked, dir); err != nil {
		// Another fetch renamed its copy first.
		if info, statErr := os.Stat(filepath.Join(dir, "lib")); statErr == nil && info.IsDir() {
			return dir, nil
		}
		return "", fmt.Errorf("static archives: %w", err)
	}
	return dir, nil
}

// LinkerFlags are the CGO_LDFLAGS that point cgo's linker at the archives
// in dir, a directory FetchArchives returned, after the flags of before,
// which a person set and which come first. A path holding a space is put
// in double quotes, as cgo splits the variable at spaces outside quotes.
func LinkerFlags(dir, before string) string {
	lib := filepath.Join(dir, "lib")
	flag := "-L" + lib
	if strings.ContainsAny(lib, " \t") {
		flag = `"` + flag + `"`
	}
	if before = strings.TrimSpace(before); before != "" {
		return before + " " + flag
	}
	return flag
}

// download writes url's body to w and returns its hex SHA-256.
func download(ctx context.Context, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the server answers %s", resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unpack writes the gzipped tarball r into dir without its first path
// element, the directory a release's tarball is named for, as `tar
// --strip-components=1` does. It takes directories and regular files, and
// refuses any other entry, and a path that would leave dir.
func unpack(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		top, rel, ok := strings.Cut(clean, "/")
		if top == "" || top == ".." || (ok && !fs.ValidPath(rel)) {
			return fmt.Errorf("entry %q lies outside the tarball's directory", hdr.Name)
		}
		if !ok || rel == "" {
			// The tarball's top directory itself.
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("entry %q is neither a directory nor a regular file", hdr.Name)
		}
	}
}
