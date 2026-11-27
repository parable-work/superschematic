// Package tasks computes with what a Tasks schema declares (D203): it
// renders the task file, reads a commit's tree through git, resolves each
// task's inputs to tracked files, and gives every task its input key and
// the plan its order. The shapes it writes are the public tasks package's.
package tasks

import (
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/tasks"
)

// The pattern a task names files by is the public tasks package's
// (tasks.Covers), and a tree's entries under a directory and the entries
// a pattern covers are the registry's, which a resolver an extension
// registers reads the same way.

// CheckPattern refuses a pattern that is no path of the repository or
// whose segments cannot be read.
func CheckPattern(pattern string) error { return tasks.CheckPattern(pattern) }

// Covers reports whether pattern covers name.
func Covers(pattern, name string) bool { return tasks.Covers(pattern, name) }

func hasMagic(pattern string) bool { return tasks.HasWildcard(pattern) }

func under(entries []registry.TaskTreeEntry, prefix string) []registry.TaskTreeEntry {
	return registry.TaskEntriesUnder(entries, prefix)
}

func match(tree registry.TaskTree, pattern string) []string {
	return registry.MatchTaskPaths(tree, pattern)
}
