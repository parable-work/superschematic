package testpaths

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The superscalar commit lives in two places: superscalar.pin, which
// scripts/superscalar-dep.sh checks out and builds, and the pseudo-version
// of github.com/parable-work/superscalar/go in every go.mod that links it.
// A bump that touches one and not the other builds the Go archive from a
// different commit than the Go binding it links against.
func TestSuperscalarPinMatchesGoModPseudoVersions(t *testing.T) {
	root := RepoRoot(t)
	pin, err := os.ReadFile(filepath.Join(root, "superscalar.pin"))
	if err != nil {
		t.Fatal(err)
	}
	var commit string
	for _, line := range strings.Split(string(pin), "\n") {
		if rest, ok := strings.CutPrefix(line, "commit="); ok {
			commit = strings.TrimSpace(rest)
		}
	}
	if len(commit) != 40 {
		t.Fatalf("superscalar.pin: want a 40-char commit= line, got %q", commit)
	}

	requireRE := regexp.MustCompile(`github\.com/parable-work/superscalar/go v0\.0\.0-\d{14}-([0-9a-f]{12})`)
	for _, mod := range []string{"go.mod", "runtime/schema/go/go.mod", "runtime/http/go/go.mod"} {
		data, err := os.ReadFile(filepath.Join(root, mod))
		if err != nil {
			t.Fatal(err)
		}
		m := requireRE.FindStringSubmatch(string(data))
		if m == nil {
			t.Errorf("%s: no pseudo-version require for superscalar/go", mod)
			continue
		}
		if !strings.HasPrefix(commit, m[1]) {
			t.Errorf("%s pins superscalar/go at %s; superscalar.pin says %s", mod, m[1], commit[:12])
		}
	}
}
