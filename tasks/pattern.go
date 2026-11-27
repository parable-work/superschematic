package tasks

import (
	"fmt"
	"path"
	"strings"
)

// A task names files by pattern, in an output's paths and in a path
// input: a slash-separated path from the repository root whose segments
// may hold `*`, `?` and `[...]`, as path.Match reads them, and which may
// hold `**` as a whole segment, for any number of segments, none
// included. A pattern that matches a directory covers every file under
// it, so `scripts/ci` and `scripts/ci/**` cover the same files. A reader
// that stores an output, or puts one in place, reads its paths with
// Covers.

// CheckPath refuses a path that is no path of the repository: an empty
// or absolute one, one with a backslash, one that is not in its shortest
// form, and one that leaves the repository or names all of it.
func CheckPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("is empty")
	case strings.Contains(p, `\`):
		return fmt.Errorf("%q holds a backslash; a path is slash-separated", p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("%q is absolute; a path starts at the repository root", p)
	case path.Clean(p) != p:
		return fmt.Errorf("%q is not in its shortest form, which is %q", p, path.Clean(p))
	case p == "." || p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("%q leaves the repository or names all of it; name the directories the task reads or writes", p)
	}
	return nil
}

// CheckPattern refuses a pattern that is no path of the repository or
// whose segments path.Match cannot read.
func CheckPattern(pattern string) error {
	if err := CheckPath(pattern); err != nil {
		return err
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "**" {
			continue
		}
		if strings.Contains(segment, "**") {
			return fmt.Errorf("%q holds ** inside a segment; ** stands for whole segments, as a/**/b", pattern)
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("%q: segment %q is no pattern: %v", pattern, segment, err)
		}
	}
	return nil
}

// Covers reports whether pattern covers name: it matches name, or it
// matches a directory above name.
func Covers(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchSegments matches a pattern's segments against a path's. A pattern
// that is used up above the path's end matches, the pattern having named
// a directory.
func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			if len(rest) == 0 {
				return true
			}
			for skip := 0; skip <= len(name); skip++ {
				if matchSegments(rest, name[skip:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pattern[0], name[0]); err != nil || !ok {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return true
}

// HasWildcard reports whether a pattern holds a wildcard.
func HasWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// LiteralPrefix is the pattern's leading segments that hold no wildcard,
// joined: every path the pattern covers is that path or sits under it.
// It is "" for a pattern whose first segment holds one.
func LiteralPrefix(pattern string) string {
	segments := strings.Split(pattern, "/")
	n := 0
	for n < len(segments) && !HasWildcard(segments[n]) {
		n++
	}
	return strings.Join(segments[:n], "/")
}
