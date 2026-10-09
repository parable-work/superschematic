package stackdeploy_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// sources writes a repository whose stack build wrote a Dockerfile, with
// its ignore file, for each of servers, and an implementation for each;
// it returns where they are.
func sources(t *testing.T, servers ...string) *stackdeploy.Sources {
	t.Helper()
	root := t.TempDir()
	src := &stackdeploy.Sources{OutputRoot: filepath.Join(root, "schemas", "dist"), RepositoryRoot: root}
	for _, server := range servers {
		dockerfile := src.Dockerfile("shop-stack", server)
		ignore := "*\n!go/" + server + "\n!schemas/dist/server/shop-stack/" + server + "\n**/.git\n"
		for path, content := range map[string]string{
			dockerfile:                   "FROM scratch\nCOPY . /src\n",
			dockerfile + ".dockerignore": ignore,
			filepath.Join(root, "go", server, "implementation.go"): "package impl\n",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# the repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// touch rewrites a file of a server's implementation.
func touch(t *testing.T, src *stackdeploy.Sources, server, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(src.RepositoryRoot, "go", server, "implementation.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func builds(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "build ") {
			out = append(out, c)
		}
	}
	return out
}

// TestDeployBuildsAWorkersImage: a worker has an image as a job has (D53):
// a deploy builds it from the Dockerfile the stack's build writes for it,
// pins it in the worker's pool and records it in the manifest.
func TestDeployBuildsAWorkersImage(t *testing.T) {
	f := newFixture(t)
	env := f.workerEnv(t, "Staging")
	f.ready(t, env)
	src := sources(t, "shop-api", "Orders", stacktest.ShipOrdersJob, stacktest.FulfilOrdersWorker)
	o := deployOptions(f, t, env, nil, &planner{to: 1}, nil)
	o.Sources = src
	m, err := stackdeploy.Deploy(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(builds(f.calls(0)), "build shop-orders-fulfil-orders: schemas/dist/server/shop-stack/shop-orders-fulfil-orders/Dockerfile") {
		t.Fatalf("the deploy built %v, not the worker", builds(f.calls(0)))
	}
	pool := f.ext.Provisioner.Rendered().Resources.Resource(stacktest.FulfilOrdersWorker + ".pool")
	if got, image := pool.Properties["image"], m.Images[stacktest.FulfilOrdersWorker]; got != image || !strings.HasPrefix(image, stacktest.FulfilOrdersWorker+"@sha256:") {
		t.Errorf("the worker's pool renders image %v, and the manifest records %s", got, image)
	}
}

// TestDeployBuildsImages: a deploy with the build's sources builds each
// server's and job's image before it changes anything, from a context its
// ignore file cuts down, and pins the image built; the next deploy builds
// only the deployables whose context changed, and --image overrides a
// build.
func TestDeployBuildsImages(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	src := sources(t, "shop-api", "Orders", stacktest.ShipOrdersJob)
	p := &planner{to: 1}

	o := deployOptions(f, t, env, nil, p, nil)
	o.Sources = src
	m, err := stackdeploy.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.calls(0)
	want := []string{
		"build Orders: schemas/dist/server/shop-stack/Orders/Dockerfile",
		"build shop-api: schemas/dist/server/shop-stack/shop-api/Dockerfile",
		"build shop-orders-ship-orders: schemas/dist/server/shop-stack/shop-orders-ship-orders/Dockerfile",
	}
	if !slices.Equal(calls[:3], want) || !strings.HasPrefix(calls[3], "render") {
		t.Fatalf("the first deploy ran:\n%s\nwant the three builds before the render", strings.Join(calls, "\n"))
	}
	job := f.ext.Provisioner.Rendered().Resources.Resource(stacktest.ShipOrdersJob + ".job")
	if got, image := job.Properties["image"], m.Images[stacktest.ShipOrdersJob]; got != image || !strings.HasPrefix(image, stacktest.ShipOrdersJob+"@sha256:") {
		t.Errorf("the job renders image %v, and the manifest records %s", got, image)
	}
	if got := f.ext.Builder.Context("shop-api"); !slices.Equal(got, []string{
		"go/shop-api/", "go/shop-api/implementation.go",
		"schemas/dist/server/shop-stack/shop-api/",
		"schemas/dist/server/shop-stack/shop-api/Dockerfile",
		"schemas/dist/server/shop-stack/shop-api/Dockerfile.dockerignore",
	}) {
		t.Errorf("shop-api's context holds %q", got)
	}
	built := m.Images["shop-api"]
	if !strings.HasPrefix(built, "shop-api@sha256:") || m.Contexts["shop-api"] != strings.TrimPrefix(built, "shop-api@") {
		t.Errorf("manifest: image %s, context %s", built, m.Contexts["shop-api"])
	}
	service := f.ext.Provisioner.Rendered().Resources.Resource("shop-api.service")
	if got := service.Properties["image"]; got != built {
		t.Errorf("shop-api.service renders image %v, want %s", got, built)
	}

	// Nothing changed: nothing is built, and each server keeps its image.
	n := len(f.ext.Provisioner.Calls())
	m2, err := stackdeploy.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if b := builds(f.calls(n)); len(b) > 0 {
		t.Errorf("a deploy with nothing changed built %v", b)
	}
	if !maps(m2.Images, m.Images) || !maps(m2.Contexts, m.Contexts) {
		t.Errorf("images %v, contexts %v; want the first deploy's", m2.Images, m2.Contexts)
	}
	if !strings.Contains(f.log.String(), "is the one shop-api@sha256:") {
		t.Errorf("the log does not say shop-api kept its image:\n%s", f.log)
	}

	// shop-api's implementation changed: only it is built. Orders is
	// given an image, which replaces its build and its context.
	touch(t, src, "shop-api", "package impl // changed\n")
	n = len(f.ext.Provisioner.Calls())
	o.Images = map[string]string{"Orders": "orders@" + digest(7)}
	m3, err := stackdeploy.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if b := builds(f.calls(n)); !slices.Equal(b, want[1:2]) {
		t.Errorf("built %v, want shop-api alone", b)
	}
	if m3.Images["shop-api"] == built || m3.Images["Orders"] != "orders@"+digest(7) {
		t.Errorf("images %v", m3.Images)
	}
	if _, ok := m3.Contexts["Orders"]; ok {
		t.Errorf("Orders keeps a context for an image it was given: %v", m3.Contexts)
	}

	// The written manifest reads back with its contexts.
	data, err := f.ext.State.ReadManifest(ctx, registry.Run{Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	back, err := stackdeploy.UnmarshalManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if !maps(back.Contexts, m3.Contexts) {
		t.Errorf("read back contexts %v, want %v", back.Contexts, m3.Contexts)
	}
}

// TestDeployBuildFailures: a build that fails stops the deploy before it
// changes anything; a rollout that fails keeps the previous image and its
// context in the manifest, so the next deploy builds again; a server with
// neither a Dockerfile nor a recorded image is refused.
func TestDeployBuildFailures(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	src := sources(t, "shop-api", "Orders", stacktest.ShipOrdersJob)
	o := deployOptions(f, t, env, nil, &planner{to: 1}, nil)
	o.Sources = src

	f.ext.Builder.Fail = map[string]error{"shop-api": errors.New("the compiler failed")}
	if _, err := stackdeploy.Deploy(ctx, o); err == nil || !strings.Contains(err.Error(), "build the image of shop-api: the compiler failed") {
		t.Fatalf("deploy = %v", err)
	}
	if calls := f.calls(0); slices.ContainsFunc(calls, func(c string) bool { return !strings.HasPrefix(c, "build ") }) {
		t.Errorf("a failed build went on to:\n%s", strings.Join(calls, "\n"))
	}
	if _, err := f.ext.State.ReadManifest(ctx, registry.Run{Environment: env}); !errors.Is(err, registry.ErrNoManifest) {
		t.Errorf("a failed build wrote a manifest: %v", err)
	}

	f.ext.Builder.Fail = nil
	first, err := stackdeploy.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}

	// Orders' wave fails: Orders keeps its image and context, and the
	// next deploy builds it again.
	touch(t, src, "Orders", "package impl // v2\n")
	f.ext.Provisioner.Fail = map[string]error{"rollout 2": errors.New("Orders is not ready")}
	m, err := stackdeploy.Deploy(ctx, o)
	if err == nil {
		t.Fatal("the failed rollout deployed")
	}
	if m.Images["Orders"] != first.Images["Orders"] || m.Contexts["Orders"] != first.Contexts["Orders"] {
		t.Errorf("after the failed rollout Orders runs %s from %s, want the first deploy's", m.Images["Orders"], m.Contexts["Orders"])
	}
	f.ext.Provisioner.Fail = nil
	n := len(f.ext.Provisioner.Calls())
	m, err = stackdeploy.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if b := builds(f.calls(n)); !slices.Equal(b, []string{"build Orders: schemas/dist/server/shop-stack/Orders/Dockerfile"}) {
		t.Errorf("the next deploy built %v, want Orders again", b)
	}
	if m.Images["Orders"] == first.Images["Orders"] {
		t.Error("Orders still runs its first image")
	}

	// A fresh run with only shop-api's Dockerfile: Orders has no image.
	f2 := newFixture(t)
	env2 := f2.env(t, "Staging")
	f2.ready(t, env2)
	o2 := deployOptions(f2, t, env2, nil, &planner{to: 1}, nil)
	o2.Sources = sources(t, "shop-api")
	if _, err := stackdeploy.Deploy(ctx, o2); err == nil || !strings.Contains(err.Error(), "no image for server Orders") ||
		!strings.Contains(err.Error(), "no Dockerfile to build one from") {
		t.Errorf("a server with nothing to build from: %v", err)
	}
	if len(f2.calls(0)) > 0 {
		t.Errorf("the refused deploy ran %v", f2.calls(0))
	}
}

// TestBuild: `stack build` builds what a deploy would, writes no manifest,
// and with the manifest of a deploy builds only what changed; --server
// narrows it and --force builds what did not change.
func TestBuild(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	src := sources(t, "shop-api", "Orders", stacktest.ShipOrdersJob)
	build := func(force bool, servers ...string) *stackdeploy.BuildResult {
		t.Helper()
		r, err := stackdeploy.Build(ctx, stackdeploy.BuildOptions{Options: f.options(t, env, nil), Sources: *src, Deployables: servers, Force: force})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := build(false)
	if !slices.Equal(r.Built, []string{"Orders", "shop-api", stacktest.ShipOrdersJob}) || len(r.Unchanged) > 0 || len(r.Images) != 3 {
		t.Fatalf("first build: %+v", r)
	}
	if flags := r.ImageFlags(); len(flags) != 3 || !strings.HasPrefix(flags[0], "Orders=orders@sha256:") || !strings.HasPrefix(flags[2], stacktest.ShipOrdersJob+"="+stacktest.ShipOrdersJob+"@sha256:") {
		t.Errorf("image flags %q", flags)
	}
	if _, err := f.ext.State.ReadManifest(ctx, registry.Run{Environment: env}); !errors.Is(err, registry.ErrNoManifest) {
		t.Errorf("a build wrote a manifest: %v", err)
	}

	o := deployOptions(f, t, env, nil, &planner{to: 1}, r.Images)
	m, err := stackdeploy.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Contexts) > 0 {
		t.Errorf("images given with --image recorded contexts %v", m.Contexts)
	}
	o.Images, o.Sources = nil, src
	if _, err := stackdeploy.Deploy(ctx, o); err != nil {
		t.Fatal(err)
	}

	touch(t, src, "shop-api", "package impl // changed\n")
	if r := build(false); !slices.Equal(r.Built, []string{"shop-api"}) || !slices.Equal(r.Unchanged, []string{"Orders", stacktest.ShipOrdersJob}) || len(r.Images) != 3 {
		t.Errorf("a build after shop-api changed: %+v", r)
	}
	if r := build(false, "Orders"); len(r.Built) > 0 || !slices.Equal(r.Unchanged, []string{"Orders"}) || len(r.Images) != 1 {
		t.Errorf("a build of Orders alone: %+v", r)
	}
	if r := build(true, "Orders"); !slices.Equal(r.Built, []string{"Orders"}) {
		t.Errorf("a forced build of Orders: %+v", r)
	}
	_, err = stackdeploy.Build(ctx, stackdeploy.BuildOptions{Options: f.options(t, env, nil), Sources: *src, Deployables: []string{"shop-db"}})
	if err == nil || !strings.Contains(err.Error(), "has no server, job or site shop-db") {
		t.Errorf("a build of a database: %v", err)
	}
	_, err = stackdeploy.Build(ctx, stackdeploy.BuildOptions{Options: f.options(t, env, nil), Sources: *sources(t, "shop-api"), Deployables: []string{"Orders"}})
	if err == nil || !strings.Contains(err.Error(), "server Orders has no Dockerfile") {
		t.Errorf("a build of a server with no Dockerfile: %v", err)
	}
}

// TestDeployGrantsNewServers: a deploy whose plan has no steps still runs
// the expand phase of a DB service whose connecting servers changed since
// the runner last ran, with the plan marked as having none, so the runner
// can give a new server its privileges; once recorded, it runs nothing.
func TestDeployGrantsNewServers(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	p := &planner{to: 1}
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(1)))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Databases["shop-db"]["shop-db"].Servers; !slices.Equal(got, []string{"Orders", "shop-api", stacktest.ShipOrdersJob}) {
		t.Fatalf("the runner ran for servers %v", got)
	}

	// The manifest of a deploy before Orders connected.
	run := registry.Run{Environment: env}
	m.Databases["shop-db"]["shop-db"].Servers = []string{"shop-api"}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ext.State.WriteManifest(ctx, run, data); err != nil {
		t.Fatal(err)
	}
	n := len(f.ext.Provisioner.Calls())
	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, nil))
	if err != nil {
		t.Fatal(err)
	}
	var migrations []string
	for _, c := range f.calls(n) {
		if strings.HasPrefix(c, "migrate") {
			migrations = append(migrations, c)
		}
	}
	if want := []string{"migrate expand shop-db: shop-db (no steps; servers Orders, shop-api, shop-orders-ship-orders)"}; !slices.Equal(migrations, want) {
		t.Errorf("ran migrations %q, want %q", migrations, want)
	}
	applied := m.Databases["shop-db"]["shop-db"]
	if !slices.Equal(applied.Servers, []string{"Orders", "shop-api", stacktest.ShipOrdersJob}) || applied.Hash != modelHash(t, 1) {
		t.Errorf("shop-db: at %s for servers %v", applied.Hash, applied.Servers)
	}

	n = len(f.ext.Provisioner.Calls())
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, nil)); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(f.calls(n), func(c string) bool { return strings.HasPrefix(c, "migrate") }) {
		t.Errorf("a deploy with nothing changed migrated:\n%s", strings.Join(f.calls(n), "\n"))
	}
}

// TestManifestReadsOlderForm: a manifest without contexts or servers, as
// a deploy before builds wrote it, reads back.
func TestManifestReadsOlderForm(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"version": 1, "stack": "shop-stack", "environment": "Staging", "run": "Staging", "status": "deployed",
		"time": "2026-10-06T12:00:00Z", "resolved": &ir.ResolvedEnvironment{},
		"images": map[string]string{"shop-api": "shop-api@" + digest(1)},
	})
	m, err := stackdeploy.UnmarshalManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Contexts != nil || m.Images["shop-api"] == "" {
		t.Errorf("manifest %+v", m)
	}
}
