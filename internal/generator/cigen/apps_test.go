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

// TestAnAppsExtensionInstallsTheBinaryThatLinksIt: a stack whose app
// names the topcoat extension, whose build the released superschematic
// refuses (D78), runs superschematic-topcoat in every job: each job
// installs the release's superschematic-topcoat tarball as it would
// superschematic's, checked against SHA256SUMS and by the version it
// reports, before anything else runs; the build, the prepare and every
// stack command run it, and the header says why. A stack whose app names
// no extension installs the released superschematic.
func TestAnAppsExtensionInstallsTheBinaryThatLinksIt(t *testing.T) {
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
	if want := "Every job runs superschematic-topcoat, the release's superschematic with the topcoat extension linked too: its build of the services root builds app Backoffice of shop-stock, which needs it."; !strings.Contains(header(data), want) {
		t.Errorf("the header does not say why the workflow runs superschematic-topcoat:\n%s", data)
	}

	w := parseWorkflow(t, data)
	install := "Install superschematic-topcoat " + version
	for _, id := range w.order {
		j := w.jobs[id]
		var names []string
		for _, s := range j.Steps {
			names = append(names, s.Name)
		}
		if want := []string{"Check out", install}; len(names) < len(want) || !slices.Equal(names[:len(want)], want) {
			t.Errorf("%s's steps start %q, want %q", id, names, want)
			continue
		}
		s := j.step(install)
		for _, want := range []string{
			`curl -fsSL -o "$dir/superschematic-topcoat_` + version + `_linux-x64.tar.gz" "$release/superschematic-topcoat_` + version + `_linux-x64.tar.gz"`,
			`(cd "$dir" && sha256sum --check --ignore-missing SHA256SUMS)`,
			`got="$("$dir/superschematic-topcoat_` + version + `_linux-x64/superschematic-topcoat" --version)"`,
			`[ "$got" = "superschematic-topcoat version ` + version + `" ] || {`,
			`echo "$dir/superschematic-topcoat_` + version + `_linux-x64" >> "$GITHUB_PATH"`,
		} {
			if !strings.Contains(s.Run, want) {
				t.Errorf("%s installs the binary without %s:\n%s", id, want, s.Run)
			}
		}
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Run, "superschematic ") || strings.Contains(s.Run, "go build -trimpath") || strings.Contains(s.Run, "BUILD_COMMIT") {
				t.Errorf("%s runs the released superschematic, or builds a binary: %s", id, s.Run)
			}
		}
	}
	if s := w.jobs["check"].step("Build the services"); s.Run != "superschematic-topcoat build-all schemas/services" {
		t.Errorf("check builds with %q", s.Run)
	}
	if s := w.jobs["check"].step("Check the Rust workspace's lockfile schemas/dist/Cargo.lock"); !strings.Contains(s.Run, "(superschematic-topcoat build-all)") {
		t.Errorf("the lockfile's fix names another binary:\n%s", s.Run)
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

	// A checkout build names no release, so the step says so and fails,
	// as superschematic's does.
	checkout := appRequest("topcoat")
	checkout.Version = ""
	files, err = cigen.GitHub().Render(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if s := parseWorkflow(t, files[0].Data).jobs["check"].step("Install superschematic-topcoat"); s == nil ||
		!strings.HasSuffix(s.Run, "generate this workflow again with a released superschematic-topcoat\" && exit 1") {
		t.Errorf("a checkout build's install step: %+v", s)
	}

	// A release binary that names no digest of the archives installs the
	// release's superschematic-topcoat, and check's archives step fails,
	// as superschematic's does.
	digestless := appRequest("topcoat")
	digestless.Archives = nil
	files, err = cigen.GitHub().Render(digestless)
	if err != nil {
		t.Fatal(err)
	}
	check := parseWorkflow(t, files[0].Data).jobs["check"]
	if check.step(install) == nil {
		t.Error("a release binary without digests installs no superschematic-topcoat")
	}
	if s := check.step("Install the static archives"); s == nil || !strings.Contains(s.Run, "names no digest of its release's static archives for linux-x64") {
		t.Errorf("a release binary without digests' archives step: %+v", s)
	}
}

// TestTheRootsAppsChooseTheBinary: every job builds the whole services root
// (build-all), so an app of another stack of the root needs the binary as
// one of the stack's own does (D78): a stack with no app of its own runs
// superschematic-topcoat when another stack of its root deploys an app
// that needs topcoat, and the header names that app and its stack.
func TestTheRootsAppsChooseTheBinary(t *testing.T) {
	req := stockRequest()
	req.RootApps = []publicregistry.CIApp{{Stack: "shop-stack", App: ir.ResolvedApp{API: "shop-stock", Name: "Backoffice", Extensions: []string{"topcoat"}}}}
	files, err := cigen.GitHub().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	w := parseWorkflow(t, files[0].Data)
	for _, id := range w.order {
		if w.jobs[id].step("Install superschematic-topcoat "+version) == nil {
			t.Errorf("%s does not install superschematic-topcoat", id)
		}
	}
	if s := w.jobs["check"].step("Build the services"); s.Run != "superschematic-topcoat build-all schemas/services" {
		t.Errorf("check builds with %q", s.Run)
	}
	if want := "builds app Backoffice of shop-stock in stack shop-stack, which needs it."; !strings.Contains(header(files[0].Data), want) {
		t.Errorf("the header does not name the root's app:\n%s", files[0].Data)
	}

	// The stack's own app and another stack's that need two extensions
	// between them are refused.
	two := appRequest("topcoat")
	two.RootApps = []publicregistry.CIApp{{Stack: "shop-stack", App: ir.ResolvedApp{API: "shop-orders", Name: "Desk", Extensions: []string{"other"}}}}
	if _, err := cigen.GitHub().Render(two); err == nil || !strings.Contains(err.Error(), "app Desk of shop-orders in stack shop-stack needs other, app Backoffice of shop-stock needs topcoat") {
		t.Errorf("two extensions between the root's apps: %v", err)
	}
}

// TestAnotherStacksAppChoosesTheBinary builds shop-stack, which deploys no
// app, in a services root that holds a second stack, stock-stack, whose
// API's app Backoffice needs topcoat (D78): the workflow's build-all
// builds stock-stack too, which the released superschematic refuses, so
// every job of shop-stack's workflow runs superschematic-topcoat. Without
// the second stack it runs superschematic.
func TestAnotherStacksAppChoosesTheBinary(t *testing.T) {
	alone := newRepo(t, withGit)
	alone.mustBuild(t)
	if data := readFile(t, filepath.Join(alone.out, filepath.FromSlash(workflow))); strings.Contains(string(data), "superschematic-topcoat") {
		t.Errorf("a root with one stack and no app runs superschematic-topcoat:\n%s", data)
	}

	r := newRepo(t, withGit, withRootStack)
	r.mustBuild(t)
	path := filepath.Join(r.out, filepath.FromSlash(workflow))
	data := readFile(t, path)
	lint(t, path)
	w := parseWorkflow(t, data)
	for _, id := range w.order {
		if w.jobs[id].step("Install superschematic-topcoat "+version) == nil {
			t.Errorf("%s does not install superschematic-topcoat", id)
		}
	}
	if s := w.jobs["check"].step("Build the services"); s == nil || s.Run != "superschematic-topcoat build-all schemas/services" {
		t.Errorf("check builds with %+v", s)
	}
	if want := "builds app Backoffice of stock-app-api in stack stock-stack, which needs it."; !strings.Contains(header(data), want) {
		t.Errorf("the header does not name stock-stack's app:\n%s", data)
	}
}

// TestAppsWhoseExtensionsNoBinaryLinksAreRefused: apps that name two
// extensions between them, and an app whose extension the release ships
// no binary of, would have the workflow's build refused on every run, so
// the renderer refuses them, saying why.
func TestAppsWhoseExtensionsNoBinaryLinksAreRefused(t *testing.T) {
	for _, tc := range []struct {
		exts []string
		want string
	}{
		{[]string{"topcoat", "other"}, "stack stock-stack: app Backoffice of shop-stock needs other, app Backoffice of shop-stock needs topcoat; one binary builds the services root in CI, and no superschematic-<extension> links more than one extension"},
		{[]string{"other"}, "stack stock-stack: app Backoffice of shop-stock needs the other extension, and superschematic's release ships no binary that links it"},
	} {
		_, err := cigen.GitHub().Render(appRequest(tc.exts...))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %s", tc.exts, err, tc.want)
		}
	}
}

// header is the comment that opens a workflow, its lines joined as one.
func header(data []byte) string {
	var words []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		words = append(words, strings.TrimSpace(strings.TrimPrefix(line, "#")))
	}
	return strings.Join(strings.Fields(strings.Join(words, " ")), " ")
}
