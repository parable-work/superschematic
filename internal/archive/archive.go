// Package archive extracts tar streams into a directory without writing
// anything outside it.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Options says what ExtractTar takes from a stream beyond directories and
// regular files.
type Options struct {
	// Gzip says the stream is gzipped.
	Gzip bool

	// StripComponents drops that many leading elements from each entry's
	// path, and from a hard link's target, as `tar --strip-components`
	// does. An entry left with no path is skipped.
	StripComponents int

	// Links extracts symbolic and hard links whose target lies inside
	// dest. Without it a link is an entry of a kind not extracted.
	Links bool

	// SkipOther skips an entry of a kind not extracted, such as the pax
	// header git archive writes. Without it such an entry is an error.
	SkipOther bool
}

// ErrOutside is the Err of an EntryError whose path, or whose link's
// target, lies outside dest.
var ErrOutside = errors.New("outside the archive")

// ErrKind is the Err of an EntryError for an entry of a kind the options
// do not extract.
var ErrKind = errors.New("neither a directory nor a regular file")

// EntryError is an entry ExtractTar refuses.
type EntryError struct {
	// Name is the entry's name as the archive gives it.
	Name string
	// Linkname is the link's target, when that is what lies outside.
	Linkname string
	// Err is ErrOutside or ErrKind.
	Err error
}

func (e *EntryError) Error() string {
	switch {
	case e.Err == ErrOutside && e.Linkname != "":
		return fmt.Sprintf("archive entry %q links to %q, outside the archive", e.Name, e.Linkname)
	case e.Err == ErrOutside:
		return fmt.Sprintf("archive entry %q is outside the archive", e.Name)
	default:
		return fmt.Sprintf("archive entry %q is %v", e.Name, e.Err)
	}
}

func (e *EntryError) Unwrap() error { return e.Err }

// maxLinks bounds the links resolve follows, as the kernel's ELOOP does.
const maxLinks = 40

// ExtractTar writes the entries of the tar stream r under dest, creating
// dest when it is missing. Every write goes through an os.Root on dest, so
// nothing lands outside it, through a link or otherwise.
//
// An entry's path must be relative, clean and slash-separated, hold no
// backslash (a separator on Windows) and be local on this platform; a
// leading "./" and a trailing "/" are allowed. A symbolic link must be
// relative and, resolved through the links already extracted, stay inside
// dest. A file is created, never overwritten, with mode 0o755 when the
// header gives it any execute bit and 0o644 otherwise.
func ExtractTar(r io.Reader, dest string, opts Options) error {
	if opts.Gzip {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extract(root, tr, hdr, opts); err != nil {
			return err
		}
	}
}

// extract writes the one entry hdr, whose body tr holds.
func extract(root *os.Root, tr *tar.Reader, hdr *tar.Header, opts Options) error {
	name, ok := entryPath(hdr.Name, opts.StripComponents)
	if !ok {
		return &EntryError{Name: hdr.Name, Err: ErrOutside}
	}
	kind := hdr.Typeflag
	switch {
	case kind == tar.TypeDir || kind == tar.TypeReg:
	case (kind == tar.TypeSymlink || kind == tar.TypeLink) && opts.Links:
	case opts.SkipOther:
		return nil
	default:
		return &EntryError{Name: hdr.Name, Err: ErrKind}
	}
	if name == "" {
		// The entry stripped away, or the top directory itself.
		if kind == tar.TypeDir || opts.StripComponents > 0 {
			return nil
		}
		return &EntryError{Name: hdr.Name, Err: ErrOutside}
	}
	osName := filepath.FromSlash(name)
	if kind == tar.TypeDir {
		return root.MkdirAll(osName, 0o755)
	}
	if err := root.MkdirAll(filepath.Dir(osName), 0o755); err != nil {
		return err
	}
	switch kind {
	case tar.TypeSymlink:
		link := hdr.Linkname
		if link == "" || strings.Contains(link, `\`) || path.IsAbs(link) || filepath.IsAbs(filepath.FromSlash(link)) {
			return &EntryError{Name: hdr.Name, Linkname: hdr.Linkname, Err: ErrOutside}
		}
		if _, ok := resolve(root, path.Dir(name)+"/"+link); !ok {
			return &EntryError{Name: hdr.Name, Linkname: hdr.Linkname, Err: ErrOutside}
		}
		return root.Symlink(filepath.FromSlash(link), osName)
	case tar.TypeLink:
		// A hard link's target is a path in the archive, not one relative
		// to the link.
		target, ok := entryPath(hdr.Linkname, opts.StripComponents)
		if ok && target != "" {
			_, ok = resolve(root, target)
		}
		if !ok || target == "" {
			return &EntryError{Name: hdr.Name, Linkname: hdr.Linkname, Err: ErrOutside}
		}
		return root.Link(filepath.FromSlash(target), osName)
	}
	mode := fs.FileMode(0o644)
	if hdr.Mode&0o111 != 0 {
		mode = 0o755
	}
	out, err := root.OpenFile(osName, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, tr)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// entryPath is the slash path name names under dest once strip leading
// elements are dropped, "" for dest itself, and false when name is no path
// inside it.
func entryPath(name string, strip int) (string, bool) {
	name = strings.TrimPrefix(strings.TrimSuffix(name, "/"), "./")
	if name == "" || name == "." {
		return "", true
	}
	if strings.Contains(name, `\`) || path.IsAbs(name) || path.Clean(name) != name ||
		!filepath.IsLocal(filepath.FromSlash(name)) {
		return "", false
	}
	for range strip {
		_, rest, ok := strings.Cut(name, "/")
		if !ok {
			return "", true
		}
		name = rest
	}
	return name, true
}

// resolve returns p, a slash path relative to root, with each symbolic
// link on it replaced by its target, and false when that leaves root.
// Past a component that does not exist yet nothing can be a link, so the
// rest is taken as written, but a ".." there is refused: a link made later
// could change what it climbs out of.
func resolve(root *os.Root, p string) (string, bool) {
	var done []string
	todo := strings.Split(p, "/")
	missing := false
	links := 0
	for len(todo) > 0 {
		elem := todo[0]
		todo = todo[1:]
		switch elem {
		case "", ".":
			continue
		case "..":
			if missing || len(done) == 0 {
				return "", false
			}
			done = done[:len(done)-1]
			continue
		}
		done = append(done, elem)
		if missing {
			continue
		}
		at := filepath.FromSlash(path.Join(done...))
		info, err := root.Lstat(at)
		if err != nil {
			missing = true
			continue
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		links++
		target, err := root.Readlink(at)
		if err != nil || links > maxLinks {
			return "", false
		}
		target = filepath.ToSlash(target)
		if path.IsAbs(target) || filepath.IsAbs(filepath.FromSlash(target)) {
			return "", false
		}
		done = done[:len(done)-1]
		todo = append(strings.Split(target, "/"), todo...)
	}
	return path.Join(done...), true
}
