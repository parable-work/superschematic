package cigen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestActionsArePinnedAsCIPinsThem holds the workflow's actions to the
// commits this repository's own CI pins, so a bump there reaches the
// generated workflows too. google-github-actions/auth is not in it.
func TestActionsArePinnedAsCIPinsThem(t *testing.T) {
	ci, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []action{actionCheckout, actionSetupGo, actionSetupBun, actionSetupNode, actionPulumi} {
		if !strings.Contains(string(ci), "uses: "+a.String()) {
			t.Errorf("ci.yml does not pin %s", a)
		}
	}
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	if !commit.MatchString(actionGoogleAuth.commit) || !strings.HasPrefix(actionGoogleAuth.release, "v") {
		t.Errorf("%s is not pinned by commit", actionGoogleAuth)
	}
}

// TestScalar: a value a plain scalar would read back as something else is
// quoted.
func TestScalar(t *testing.T) {
	for in, want := range map[string]string{
		"main":                        "main",
		"release/v1":                  "release/v1",
		"1.26.4":                      `"1.26.4"`,
		"false":                       `"false"`,
		"On":                          `"On"`,
		"":                            `""`,
		"!cancelled()":                `"!cancelled()"`,
		"a: b":                        `"a: b"`,
		"a #b":                        `"a #b"`,
		"lts/*":                       "lts/*",
		"${{ github.event.number }}":  "${{ github.event.number }}",
		"github.event.action != 'x'":  "github.event.action != 'x'",
		`say "hi" <now>`:              `say "hi" <now>`,
		"-x":                          `"-x"`,
		"trailing:":                   `"trailing:"`,
		"projects/1/providers/github": "projects/1/providers/github",
	} {
		if got := scalar(in); got != want {
			t.Errorf("scalar(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestJobID: an environment's name as a job id.
func TestJobID(t *testing.T) {
	for in, want := range map[string]string{
		"Staging":       "staging",
		"PreviewEnv":    "preview-env",
		"Stage2":        "stage2",
		"EU_West":       "eu_west",
		"$Odd":          "odd",
		"already-kebab": "already-kebab",
	} {
		if got := jobID(in); got != want {
			t.Errorf("jobID(%q) = %q, want %q", in, got, want)
		}
	}
}
