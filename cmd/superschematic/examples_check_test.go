package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/cli"
)

// otherBinaryExamples are the examples a binary other than this one
// builds, by the extension it links, with why: this binary refuses them by
// design, and their own smokes build them.
var otherBinaryExamples = map[string]string{
	"acme-schematic": "cmd/acme-schematic builds it, linking the apikey auth provider",
}

// checkReport is the part of `check --json`'s document the test reads.
type checkReport struct {
	OK       bool             `json:"ok"`
	Services []checkedService `json:"services"`
}

// checkedService is a service `check --json` reports.
type checkedService struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// TestExamplesCheck runs `superschematic check` over every example's
// services, as a reader of its README does, so a schema config this binary
// refuses cannot land while the loader tests, which load each service
// directly, pass. Each sentinel the check writes must be the one committed.
// An example with no Stack service is also built with build-all, into an
// output root of the test's: its server imports the packages build-all
// writes. A stack's build writes an API's missing implementation scaffold
// into the example, so the stack examples build in tests of their own.
func TestExamplesCheck(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	roots, err := filepath.Glob(filepath.Join(repoRoot, "examples", "*", "schemas", "services"))
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) == 0 {
		t.Fatal("no examples/*/schemas/services found")
	}
	for _, services := range roots {
		example := filepath.Base(filepath.Dir(filepath.Dir(services)))
		t.Run(example, func(t *testing.T) {
			if why, ok := otherBinaryExamples[example]; ok {
				t.Skip(why)
			}
			sentinels := readSentinels(t, services)
			t.Cleanup(func() { restoreSentinels(t, sentinels) })

			stdout, stderr, err := execute("check", services, "--json")
			var report checkReport
			if decodeErr := json.Unmarshal([]byte(stdout), &report); decodeErr != nil {
				t.Fatalf("superschematic check %s: %v; decoding its report: %v\n%s%s", services, err, decodeErr, stdout, stderr)
			}
			if err != nil || !report.OK {
				t.Fatalf("superschematic check %s: %v\n%s%s", services, err, stdout, stderr)
			}
			checkSentinels(t, sentinels, "check")

			if slices.ContainsFunc(report.Services, func(s checkedService) bool { return s.Kind == "Stack" }) {
				return
			}
			stdout, stderr, err = execute("build-all", services, "--out", t.TempDir())
			if err != nil {
				t.Fatalf("superschematic build-all %s: %v\n%s%s", services, err, stdout, stderr)
			}
			checkSentinels(t, sentinels, "build-all")
		})
	}
}

// execute runs the installed binary's command line with args, and returns
// what it wrote to stdout and stderr, apart, and its error.
func execute(args ...string) (string, string, error) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

// readSentinels reads each service's committed sentinel under services,
// src/service.generated.ts, by its path.
func readSentinels(t *testing.T, services string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(services, "*", "src", "service.generated.ts"))
	if err != nil {
		t.Fatal(err)
	}
	sentinels := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sentinels[path] = data
	}
	return sentinels
}

// checkSentinels fails for each sentinel command rewrote to differ from
// the committed one: the committed one is stale.
func checkSentinels(t *testing.T, sentinels map[string][]byte, command string) {
	t.Helper()
	for path, want := range sentinels {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s rewrote %s, so the committed sentinel is stale: run superschematic check in the example and commit it\ngot:\n%s\nwant:\n%s", command, path, got, want)
		}
	}
}

// restoreSentinels writes each committed sentinel back, so a failing run
// leaves the example as it found it.
func restoreSentinels(t *testing.T, sentinels map[string][]byte) {
	t.Helper()
	for path, data := range sentinels {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Error(err)
		}
	}
}
