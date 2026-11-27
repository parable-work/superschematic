// Package tasks computes with what a Tasks schema declares (D203): it
// renders the task file, reads a commit's tree through git, resolves each
// task's inputs to tracked files, and gives every task its input key and
// the plan its order. The shapes it writes are the public tasks package's.
package tasks

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
)

// A pattern is a slash-separated path from the repository root whose
// segments may hold `*`, `?` and `[...]`, as path.Match reads them, and
// which may hold `**` as a whole segment, for any number of segments, none
// included. A pattern that matches a directory covers every file under it,
// so `scripts/ci` and `scripts/ci/**` cover the same files.

// CheckPattern refuses a pattern that is no path of the repository or
// whose segments path.Match cannot read.
func CheckPattern(pattern string) error {
	if err := registry.CheckTaskPath(pattern); err != nil {
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
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"), true)
}

// matchSegments matches a pattern's segments against a path's. With
// prefix set, a pattern that is used up above the path's end matches, the
// pattern having named a directory.
func matchSegments(pattern, name []string, prefix bool) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			if len(rest) == 0 {
				return true
			}
			for skip := 0; skip <= len(name); skip++ {
				if matchSegments(rest, name[skip:], prefix) {
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
	return len(name) == 0 || prefix
}

// hasMagic reports whether a pattern holds a wildcard.
func hasMagic(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// literalPrefix is the pattern's leading segments that hold no wildcard,
// joined: every path the pattern covers is that path or sits under it.
func literalPrefix(pattern string) string {
	segments := strings.Split(pattern, "/")
	n := 0
	for n < len(segments) && !hasMagic(segments[n]) {
		n++
	}
	return strings.Join(segments[:n], "/")
}

// under returns the entries of a sorted list that are prefix or sit under
// it, found by search and not by a scan of the whole tree.
func under(entries []registry.TaskTreeEntry, prefix string) []registry.TaskTreeEntry {
	if prefix == "" {
		return entries
	}
	var out []registry.TaskTreeEntry
	if i := sort.Search(len(entries), func(i int) bool { return entries[i].Path >= prefix }); i < len(entries) && entries[i].Path == prefix {
		out = append(out, entries[i])
	}
	dir := prefix + "/"
	start := sort.Search(len(entries), func(i int) bool { return entries[i].Path >= dir })
	end := start
	for end < len(entries) && strings.HasPrefix(entries[end].Path, dir) {
		end++
	}
	return append(out, entries[start:end]...)
}

// match returns the paths of the tree's entries that pattern covers.
func match(tree registry.TaskTree, pattern string) []string {
	segments := strings.Split(pattern, "/")
	var out []string
	for _, entry := range under(tree.Entries(), literalPrefix(pattern)) {
		if matchSegments(segments, strings.Split(entry.Path, "/"), true) {
			out = append(out, entry.Path)
		}
	}
	return out
}
