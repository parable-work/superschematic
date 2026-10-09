package stackdeploy

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/scanner"
	"time"
)

// A server's image builds from a context: the repository root, cut down by
// the ignore file beside the generated Dockerfile (`Dockerfile.dockerignore`,
// docs/stack-model.md, section 8.2), read by the rules Docker's BuildKit
// reads it by. The deploy writes the context as a gzipped tarball the
// target's builder takes, and names it by the digest of its tar stream.
// Every entry is written in path order, at the epoch, owned by root, with
// mode 0644, or 0755 for a directory or an executable file, so the same
// files give the same digest on any machine and from any checkout. A
// deploy builds a server's image again only when that digest differs from
// the one its manifest records for the image it runs.

// Context is a build context written to an archive.
type Context struct {
	// Digest is `sha256:` and the hex SHA-256 of the tar stream, before
	// compression.
	Digest string

	// Dockerfile is the Dockerfile's path inside the context.
	Dockerfile string

	// Files counts the regular files and links it holds, and Size the
	// bytes of its tar stream.
	Files int
	Size  int64
}

// WriteContext writes, to w as a gzipped tarball, the build context of the
// Dockerfile at dockerfile, whose context directory is root. The ignore
// file is `<Dockerfile>.dockerignore` beside the Dockerfile, else
// `.dockerignore` at root, else none. As Docker's client does, the context
// always holds the Dockerfile and its ignore file, whatever the ignore
// file says. It refuses a context that lacks a path the ignore file takes
// in by name (checkTakenIn).
func WriteContext(w io.Writer, root, dockerfile string) (*Context, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dockerfile, err = filepath.Abs(dockerfile)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, dockerfile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("the Dockerfile %s lies outside its build context %s", dockerfile, root)
	}
	if info, err := os.Stat(dockerfile); err != nil {
		return nil, fmt.Errorf("the Dockerfile: %w", err)
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the Dockerfile %s is not a file", dockerfile)
	}
	out := &Context{Dockerfile: filepath.ToSlash(rel)}
	always := map[string]bool{out.Dockerfile: true}
	ignorePath := dockerfile + ".dockerignore"
	if _, err := os.Stat(ignorePath); err != nil {
		ignorePath = filepath.Join(root, ".dockerignore")
	}
	var patterns []string
	switch data, err := os.ReadFile(ignorePath); {
	case err == nil:
		if patterns, err = readIgnoreFile(data); err != nil {
			return nil, fmt.Errorf("%s: %w", ignorePath, err)
		}
		r, _ := filepath.Rel(root, ignorePath)
		always[filepath.ToSlash(r)] = true
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	if err := checkTakenIn(root, ignorePath, patterns); err != nil {
		return nil, err
	}
	m, err := newIgnoreMatcher(patterns)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ignorePath, err)
	}

	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	sum := sha256.New()
	counter := &countWriter{}
	tw := tar.NewWriter(io.MultiWriter(gz, sum, counter))
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		r, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		r = filepath.ToSlash(r)
		excluded := m.excludes(r) && !always[r]
		if entry.IsDir() {
			if excluded {
				if m.canSkip(r) && !holdsAny(always, r) {
					return filepath.SkipDir
				}
				return nil
			}
			return writeEntry(tw, p, r, entry)
		}
		if excluded {
			return nil
		}
		if entry.Type().IsRegular() || entry.Type()&fs.ModeSymlink != 0 {
			out.Files++
			return writeEntry(tw, p, r, entry)
		}
		return nil // a socket, a device or a pipe: never part of a context
	})
	if err != nil {
		return nil, fmt.Errorf("write the build context of %s: %w", out.Dockerfile, err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	out.Digest = "sha256:" + hex.EncodeToString(sum.Sum(nil))
	out.Size = counter.n
	return out, nil
}

// checkTakenIn refuses a context that lacks a path an exception pattern of
// the ignore file at ignorePath names with no wildcard, as a generated
// ignore file names each directory its Dockerfile builds from, or that
// holds the path under a symbolic link, which a context carries as a link
// and not the files it points at. Either way the build would fail at a
// COPY in the target's builder, after the upload; a superscalar checkout
// that a CI runner never made, or one linked in from outside the
// repository, is the usual cause.
func checkTakenIn(root, ignorePath string, patterns []string) error {
	for _, p := range patterns {
		name, ok := strings.CutPrefix(p, "!")
		if !ok || name == "" || name == "." || strings.ContainsAny(name, `*?[\`) {
			continue
		}
		at := root
		for _, part := range strings.Split(name, "/") {
			at = filepath.Join(at, part)
			info, err := os.Lstat(at)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return fmt.Errorf("the build context %s holds no %s, which %s takes in for the Dockerfile to build from", root, name, ignorePath)
			case err != nil:
				return err
			case info.Mode()&fs.ModeSymlink != 0:
				link, _ := filepath.Rel(root, at)
				return fmt.Errorf("%s, which %s takes in for the Dockerfile to build from, lies under %s, a symbolic link, and a build context holds a link and not the files it points at; put the files in %s itself", name, ignorePath, filepath.ToSlash(link), root)
			}
		}
	}
	return nil
}

// holdsAny reports whether a path of always lies under dir.
func holdsAny(always map[string]bool, dir string) bool {
	for p := range always {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

// epoch is every entry's time.
var epoch = time.Unix(0, 0).UTC()

// writeEntry writes one entry with its time, owner and mode normalized.
func writeEntry(tw *tar.Writer, p, name string, entry fs.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: name, ModTime: epoch, Mode: 0o644}
	switch {
	case info.IsDir():
		hdr.Typeflag = tar.TypeDir
		hdr.Name += "/"
		hdr.Mode = 0o755
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = filepath.ToSlash(target)
		hdr.Mode = 0o777
	default:
		hdr.Typeflag = tar.TypeReg
		hdr.Size = info.Size()
		if info.Mode()&0o111 != 0 {
			hdr.Mode = 0o755
		}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	n, err := io.Copy(tw, f)
	if err != nil {
		return err
	}
	if n != hdr.Size {
		return fmt.Errorf("%s changed while it was read", name)
	}
	return nil
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// readIgnoreFile reads an ignore file's patterns as Docker reads them: a
// line starting with # is a comment, blank lines are skipped, each pattern
// is trimmed and cleaned, a leading slash is dropped, and ! marks an
// exception.
func readIgnoreFile(data []byte) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		pattern := strings.TrimSpace(line)
		if pattern == "" {
			continue
		}
		invert := pattern[0] == '!'
		if invert {
			pattern = strings.TrimSpace(pattern[1:])
		}
		if pattern != "" {
			pattern = path.Clean(filepath.ToSlash(pattern))
			if len(pattern) > 1 && pattern[0] == '/' {
				pattern = pattern[1:]
			}
		}
		if invert {
			pattern = "!" + pattern
		}
		out = append(out, pattern)
	}
	return out, sc.Err()
}

// ignorePattern is one compiled pattern of an ignore file.
type ignorePattern struct {
	exception bool // a ! pattern, which takes paths back in
	text      string
	dirs      []string
	re        *regexp.Regexp
}

// ignoreMatcher decides, as BuildKit does, whether an ignore file leaves a
// path out of the context: each pattern in turn matches the path or one of
// its parent directories, and the last pattern that matches decides.
type ignoreMatcher struct {
	patterns []*ignorePattern
}

func newIgnoreMatcher(patterns []string) (*ignoreMatcher, error) {
	m := &ignoreMatcher{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = path.Clean(p)
		ip := &ignorePattern{}
		if p[0] == '!' {
			if len(p) == 1 {
				return nil, errors.New(`illegal exception pattern: "!"`)
			}
			ip.exception = true
			p = p[1:]
		}
		if _, err := path.Match(p, "."); err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p, err)
		}
		ip.text = p
		ip.dirs = strings.Split(p, "/")
		re, err := compileIgnorePattern(p)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p, err)
		}
		ip.re = re
		m.patterns = append(m.patterns, ip)
	}
	return m, nil
}

// compileIgnorePattern turns a pattern into the regular expression
// BuildKit's pattern matcher builds from it: `*` is any run of characters
// but a slash, `?` one character but a slash, `**` any number of whole
// directories, `\` escapes the next character, and a bracket expression is
// a character class.
func compileIgnorePattern(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	var sc scanner.Scanner
	sc.Init(strings.NewReader(pattern))
	sc.Mode = 0
	sc.Error = func(*scanner.Scanner, string) {}
	for sc.Peek() != scanner.EOF {
		ch := sc.Next()
		switch {
		case ch == '*':
			if sc.Peek() != '*' {
				b.WriteString("[^/]*")
				continue
			}
			sc.Next()
			if sc.Peek() == '/' {
				sc.Next()
			}
			if sc.Peek() == scanner.EOF {
				b.WriteString(".*")
			} else {
				b.WriteString("(.*/)?")
			}
		case ch == '?':
			b.WriteString("[^/]")
		case strings.ContainsRune(".+()|{}$", ch):
			b.WriteString(`\` + string(ch))
		case ch == '\\':
			if sc.Peek() != scanner.EOF {
				b.WriteString(regexp.QuoteMeta(string(sc.Next())))
			} else {
				b.WriteString(`\\`)
			}
		case ch == '[' || ch == ']':
			b.WriteRune(ch)
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// excludes reports whether the ignore file leaves the slash-separated
// path p out of the context.
func (m *ignoreMatcher) excludes(p string) bool {
	excluded := false
	parents := []string{}
	if dir := path.Dir(p); dir != "." {
		parents = strings.Split(dir, "/")
	}
	for _, pattern := range m.patterns {
		// An exception only matters to a path that is out, and any other
		// pattern only to one that is in.
		if pattern.exception != excluded {
			continue
		}
		match := pattern.re.MatchString(p)
		for i := range parents {
			if match {
				break
			}
			match = pattern.re.MatchString(strings.Join(parents[:i+1], "/"))
		}
		if match {
			excluded = !pattern.exception
		}
	}
	return excluded
}

// canSkip reports whether nothing under the excluded directory dir can be
// taken back in, so the walk need not enter it: no exception pattern holds
// `**`, or names more directories than dir does with its first ones
// matching dir's.
func (m *ignoreMatcher) canSkip(dir string) bool {
	parts := strings.Split(dir, "/")
	for _, pattern := range m.patterns {
		if !pattern.exception {
			continue
		}
		if strings.Contains(pattern.text, "**") {
			return false
		}
		if len(pattern.dirs) <= len(parts) {
			continue
		}
		prefix := true
		for i, part := range parts {
			if ok, _ := path.Match(pattern.dirs[i], part); !ok {
				prefix = false
				break
			}
		}
		if prefix {
			return false
		}
	}
	return true
}
