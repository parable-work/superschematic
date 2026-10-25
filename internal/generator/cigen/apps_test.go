package cigen_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/cigen"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

// appRequest is stockRequest's stack with shop-stock's app Backoffice
// (D78), which names the extensions exts.
func appRequest(exts ...string) publicregistry.CIRequest {
	req := stockRequest()
	env := *req.Environments[0].Environment
	env.Deployables = append(slices.Clone(env.Deployables), &ir.ResolvedDeployable{
		Name: "shop-stock-backoffice", Kind: ir.DeployableApp, Language: publicregistry.APILanguageRust,
		Services: []ir.ServiceRef{{Name: "shop-stock", Kind: ir.SchemaKindAPI}},
		App:      &ir.ResolvedApp{API: "shop-stock", Name: "Backoffice", Extensions: exts, Enabled: true},
	})
	req.Environments[0].Environment = &env
	return req
}

// TestAnAppsExtensionBuildsTheBinaryThatLinksIt: a stack whose app names
// the topcoat extension, whose build the released superschematic refuses
// (D78), runs superschematic-topcoat in every job: each job downloads the
// release's superschematic, checked against SHA256SUMS, for the commit its
// BUILD_COMMIT names, fetches that commit by its hash, and builds
// extensions/topcoat's command against the release's static archives,
// with their digests linked in, before anything else runs; the build, the
// prepare and every stack command run it. check sets up neither Go nor
// the archives a second time. A stack whose app names no extension
// installs the released superschematic.
func TestAnAppsExtensionBuildsTheBinaryThatLinksIt(t *testing.T) {
	none, err := cigen.GitHub().Render(appRequest())
	if err != nil {
		t.Fatal(err)
	}
	for id, j := range parseWorkflow(t, none[0].Data).jobs {
		if j.step("Install superschematic "+version) == nil {
			t.Errorf("%s installs no released superschematic for an app that names no extension", id)
		}
		for _, s := range j.Steps {
			if strings.Contains(s.Run, "superschematic-topcoat") {
				t.Errorf("%s runs superschematic-topcoat for an app that names no extension: %s", id, s.Run)
			}
		}
	}

	files, err := cigen.GitHub().Render(appRequest("topcoat"))
	if err != nil {
		t.Fatal(err)
	}
	data := files[0].Data
	path := filepath.Join(t.TempDir(), "stock-stack.yml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	lint(t, path)

	w := parseWorkflow(t, data)
	download := "Download superschematic " + version + ", whose BUILD_COMMIT superschematic-topcoat builds from"
	build := "Build superschematic-topcoat " + version + " from the release's commit"
	for _, id := range w.order {
		j := w.jobs[id]
		var names []string
		for _, s := range j.Steps {
			names = append(names, s.Name)
		}
		if want := []string{"Check out", download, "Install the static archives", "Set up Go", build}; len(names) < len(want) || !slices.Equal(names[:len(want)], want) {
			t.Errorf("%s's steps start %q, want %q", id, names, want)
			continue
		}
		for _, name := range []string{"Install the static archives", "Set up Go"} {
			if n := strings.Count(strings.Join(names, "\n")+"\n", name+"\n"); n != 1 {
				t.Errorf("%s has %d steps %q: %q", id, n, name, names)
			}
		}
		if s := j.step(download); !strings.Contains(s.Run, `sha256sum --check --ignore-missing SHA256SUMS`) ||
			!strings.Contains(s.Run, "superschematic_"+version+"_linux-x64.tar.gz") || strings.Contains(s.Run, "GITHUB_PATH") {
			t.Errorf("%s downloads the release with:\n%s", id, s.Run)
		}
		s := j.step(build)
		for _, want := range []string{
			`commit="$(cat "$dir/superschematic_` + version + `_linux-x64/BUILD_COMMIT")"`,
			`git -C "$src" fetch -q --depth 1 https://github.com/parable-work/superschematic "$commit"`,
			`test "$(git -C "$src" rev-parse HEAD)" = "$commit"`,
			`cd "$src/extensions/topcoat" && CGO_ENABLED=1 go build -trimpath -buildvcs=false`,
			"-X github.com/parable-work/superschematic/internal/release.archiveDigests=linux-x64=" + strings.Repeat("4", 64),
			`-o "$dir/bin/superschematic-topcoat" ./cmd/superschematic-topcoat`,
			`echo "$dir/bin" >> "$GITHUB_PATH"`,
		} {
			if !strings.Contains(s.Run, want) {
				t.Errorf("%s builds the binary without %s:\n%s", id, want, s.Run)
			}
		}
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Run, "superschematic ") {
				t.Errorf("%s runs the released superschematic: %s", id, s.Run)
			}
		}
	}
	if s := w.jobs["check"].step("Build the services"); s.Run != "superschematic-topcoat build-all schemas/services" {
		t.Errorf("check builds with %q", s.Run)
	}
	if s := w.jobs["prepare"].step("Prepare Staging"); !strings.HasPrefix(s.Run, "superschematic-topcoat stack prepare Staging ") {
		t.Errorf("prepare prepares with %q", s.Run)
	}
	for _, id := range []string{"plan-staging", "deploy-staging"} {
		var commands int
		for _, s := range w.jobs[id].Steps {
			if strings.HasPrefix(s.Run, "superschematic-topcoat stack ") {
				commands++
			}
		}
		if commands == 0 {
			t.Errorf("%s runs no stack command with superschematic-topcoat", id)
		}
	}

	// A checkout build names no release, so the step says so and fails.
	checkout := appRequest("topcoat")
	checkout.Version = ""
	files, err = cigen.GitHub().Render(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if s := parseWorkflow(t, files[0].Data).jobs["check"].step("Install superschematic-topcoat"); s == nil || !strings.Contains(s.Run, "exit 1") {
		t.Errorf("a checkout build's install step: %+v", s)
	}
}

// TestAppsWhoseExtensionsNoBinaryLinksAreRefused: apps that name two
// extensions between them, and an app whose extension's binary
// superschematic's repository does not hold, would have the workflow's
// build refused on every run, so the renderer refuses them, saying why.
func TestAppsWhoseExtensionsNoBinaryLinksAreRefused(t *testing.T) {
	for _, tc := range []struct {
		exts []string
		want string
	}{
		{[]string{"topcoat", "other"}, "stack stock-stack: app Backoffice of shop-stock needs other, app Backoffice of shop-stock needs topcoat; one binary builds the stack in CI, and no superschematic-<extension> links more than one extension"},
		{[]string{"other"}, "stack stock-stack: app Backoffice of shop-stock needs the other extension, and superschematic's release neither ships nor holds the source of a binary that links it"},
	} {
		_, err := cigen.GitHub().Render(appRequest(tc.exts...))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %s", tc.exts, err, tc.want)
		}
	}
}
