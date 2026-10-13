package stackinit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// IgnoreEdit is what EnsureIgnoreRules did to the ignore file.
type IgnoreEdit struct {
	// File is the .gitignore that covers the schemas root, which it
	// edited or created.
	File string

	// Created is whether the file is new.
	Created bool

	// Added are the lines it appended, in order; Replaced maps each line
	// it rewrote in place to its new text. Both empty: the rules were in
	// place.
	Added    []string
	Replaced map[string]string

	// StillIgnored, when set, is the rule git names for ignoring the
	// output root's lockfile even after the edit, as `git check-ignore
	// --verbose` prints it: one of the project's own the edit cannot
	// overrule.
	StillIgnored string
}

// Changed reports whether the edit wrote the file.
func (e *IgnoreEdit) Changed() bool { return len(e.Added) > 0 || len(e.Replaced) > 0 }

// ignoreComment heads the lines EnsureIgnoreRules appends.
const ignoreComment = "# The output root is generated, but for its Bun workspace's lockfile, which the\n# project commits (D51); .superschematic holds this machine's local secrets and keys."

// EnsureIgnoreRules adds the rules a stack needs to the .gitignore that
// covers schemasRoot: the nearest one at or above it, up to the root of
// its git repository, else a new one in schemasRoot. The rules, relative
// to that file and anchored to the schemas root, ignore the output root's
// contents but take back its lockfile, `dist/*` and `!dist/bun.lock`,
// since git cannot take back a file under an ignored directory (D51,
// amended), and ignore `.superschematic`. A rule that ignores the output
// root itself, anchored to the schemas root, is rewritten in place to
// ignore its contents. When git still ignores the lockfile, because a
// broader rule ignores the output root, the file takes the directory back
// (`!dist/`) after it. Each rule is added once: a second run changes
// nothing.
func EnsureIgnoreRules(schemasRoot string) (*IgnoreEdit, error) {
	schemasRoot, err := filepath.Abs(schemasRoot)
	if err != nil {
		return nil, err
	}
	file, err := coveringIgnoreFile(schemasRoot)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(filepath.Dir(file), schemasRoot)
	if err != nil {
		return nil, err
	}
	prefix := "/"
	if rel != "." {
		prefix += filepath.ToSlash(rel) + "/"
	}
	contents := prefix + "dist/*"
	lockfile := "!" + prefix + "dist/bun.lock"
	state := prefix + ".superschematic/"

	edit := &IgnoreEdit{File: file, Replaced: map[string]string{}}
	data, err := os.ReadFile(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		edit.Created = true
	case err != nil:
		return nil, err
	}
	lines := splitLines(string(data))

	// A rule on the output root itself, anchored to the schemas root,
	// becomes the rule on its contents.
	directory := []string{prefix + "dist", prefix + "dist/"}
	if rel != "." {
		directory = append(directory, strings.TrimPrefix(prefix, "/")+"dist", strings.TrimPrefix(prefix, "/")+"dist/")
	}
	for i, line := range lines {
		if trimmed := strings.TrimSpace(line); slices.Contains(directory, trimmed) {
			edit.Replaced[trimmed] = contents
			lines[i] = contents
		}
	}
	has := func(rules ...string) bool {
		for _, line := range lines {
			if slices.Contains(rules, strings.TrimSpace(line)) {
				return true
			}
		}
		return false
	}
	var missing []string
	if !has(contents) {
		missing = append(missing, contents)
	}
	if !has(lockfile) {
		missing = append(missing, lockfile)
	}
	stateRules := []string{state, strings.TrimSuffix(state, "/")}
	if rel == "." {
		stateRules = append(stateRules, ".superschematic", ".superschematic/")
	}
	if !has(stateRules...) {
		missing = append(missing, state)
	}
	lines = appendRules(lines, missing)
	edit.Added = missing
	if edit.Changed() {
		if err := writeLines(file, lines); err != nil {
			return nil, err
		}
	}

	// A broader rule that ignores the output root hides the lockfile from
	// the rules above; the schemas root takes the directory back.
	rule, ignored := ignoredBy(schemasRoot, "dist/"+lockfileName)
	if !ignored {
		return edit, nil
	}
	if takeBack := "!" + prefix + "dist/"; !has(takeBack) {
		before := slices.Clone(lines)
		if err := writeLines(file, appendRules(lines, []string{takeBack})); err != nil {
			return nil, err
		}
		if _, still := ignoredBy(schemasRoot, "dist/"+lockfileName); !still {
			edit.Added = append(edit.Added, takeBack)
			return edit, nil
		}
		// Another rule of the project's ignores the lockfile, which the
		// take-back does not change: the file goes back as it was.
		if err := writeLines(file, before); err != nil {
			return nil, err
		}
	}
	edit.StillIgnored = rule
	return edit, nil
}

// lockfileName is the Bun workspace's lockfile at the output root
// (tsgen.LockfileName).
const lockfileName = "bun.lock"

// appendRules appends rules after a comment, separated from what comes
// before by a blank line. A file that holds the comment already, from an
// earlier run, takes the rules after it with no second comment.
func appendRules(lines, rules []string) []string {
	if len(rules) == 0 {
		return lines
	}
	commented := false
	for _, line := range lines {
		if line == strings.SplitN(ignoreComment, "\n", 2)[0] {
			commented = true
		}
	}
	if !commented {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(ignoreComment, "\n")...)
	}
	return append(lines, rules...)
}

// coveringIgnoreFile is the .gitignore nearest schemasRoot, at or above it
// and no higher than the root of its git repository, or schemasRoot's own
// when there is none. Outside a repository only schemasRoot's own counts.
func coveringIgnoreFile(schemasRoot string) (string, error) {
	repoRoot := repositoryRoot(schemasRoot)
	for dir := schemasRoot; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ".gitignore")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if repoRoot == "" || dir == repoRoot || filepath.Dir(dir) == dir {
			break
		}
	}
	return filepath.Join(schemasRoot, ".gitignore"), nil
}

// repositoryRoot returns the directory that holds the .git of the
// repository dir is in, or "" outside one.
func repositoryRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ignoredBy asks git whether its rules ignore path, relative to dir, and
// names the rule when they do. Outside a repository, or without git, it
// reports nothing ignored: no rule applies.
func ignoredBy(dir, path string) (string, bool) {
	if repositoryRoot(dir) == "" {
		return "", false
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", false
	}
	check := exec.Command("git", "check-ignore", "-q", "--no-index", "--", path)
	check.Dir = dir
	if check.Run() != nil {
		// 1: not ignored; 128: not a repository after all.
		return "", false
	}
	verbose := exec.Command("git", "check-ignore", "--verbose", "--no-index", "--", path)
	verbose.Dir = dir
	out, err := verbose.Output()
	if err != nil {
		return "", true
	}
	rule, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	return rule, true
}

// splitLines splits a file into its lines, without the last line's
// ending.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

func writeLines(path string, lines []string) error {
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
