package gcp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// fakeCloud is Google Cloud in memory. It records each call that changes
// something, so a test reads what a second run changed, and each build
// and job run in log too, when set, beside the provisioner's calls.
type fakeCloud struct {
	mu      sync.Mutex
	changes []string
	enabled map[string][]string
	buckets map[string]string
	keys    map[string]bool
	objects map[string][]byte
	secrets map[string]*fakeSecret

	// images holds each pushed image's digest by `<repository>:<tag>`;
	// builds and jobs what RunBuild and EnsureJob were last given;
	// graphJobs the image of each Cloud Run job the graph made, which a
	// test sets, since the fake provisioner makes none (D52); runs the
	// arguments of each job run, nil for a graph job's; stderr what each
	// failed execution wrote to stderr, by its name, and stderrReads each
	// read of it.
	images      map[string]string
	builds      []BuildSpecRecord
	jobs        map[string]gcp.JobSpec
	graphJobs   map[string]string
	runs        [][]string
	stderr      map[string][]string
	stderrReads []stderrRead

	// failRun fails the next job run whose arguments, or a graph job's
	// name, hold the string, and failBuild every build of an image that
	// starts with the string.
	// failWait fails the wait for the next job run, which the fake starts;
	// failStderr fails each read of an execution's stderr.
	failRun    map[string]fakeFailure
	failBuild  map[string]string
	failWait   error
	failStderr error

	// numbers holds each project's number.
	numbers map[string]string

	// served holds the content type and Cache-Control of each object a
	// site's bucket serves, by `<bucket>/<object>` (D55); absent names the
	// buckets that do not exist, which every other bucket does.
	served map[string][2]string
	absent map[string]bool

	log *stacktest.FakeProvisioner
}

// fakeFailure is an execution that fails: Cloud Run's message, and the
// lines its task wrote to stderr.
type fakeFailure struct {
	message string
	stderr  []string
}

// stderrRead is a read of an execution's stderr: the lines match
// selects, all of them when it is empty.
type stderrRead struct {
	execution string
	match     string
	wait      time.Duration
}

// BuildSpecRecord is a build the fake ran.
type BuildSpecRecord struct {
	Project, Region string
	gcp.BuildSpec
}

type fakeSecret struct {
	versions  [][]byte
	accessors []string
}

var _ gcp.Cloud = (*fakeCloud)(nil)

func newFakeCloud() *fakeCloud {
	return &fakeCloud{
		enabled: map[string][]string{}, buckets: map[string]string{}, keys: map[string]bool{},
		objects: map[string][]byte{}, secrets: map[string]*fakeSecret{},
		images: map[string]string{}, jobs: map[string]gcp.JobSpec{}, graphJobs: map[string]string{}, stderr: map[string][]string{},
		failRun: map[string]fakeFailure{}, failBuild: map[string]string{},
		numbers: map[string]string{"acme-staging": "123456789012", "acme-prod": "210987654321"},
		served:  map[string][2]string{}, absent: map[string]bool{},
	}
}

func (c *fakeCloud) record(format string, args ...any) {
	if c.log != nil {
		c.log.Record(format, args...)
	}
}

func (c *fakeCloud) ImageDigest(_ context.Context, image string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if digest, ok := c.images[image]; ok {
		return digest, nil
	}
	return "", fmt.Errorf("%s: %w", image, fs.ErrNotExist)
}

func (c *fakeCloud) RunBuild(_ context.Context, project, region string, spec gcp.BuildSpec) (*gcp.BuildResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.builds = append(c.builds, BuildSpecRecord{project, region, spec})
	c.record("cloud build %s", spec.Image)
	if _, ok := c.objects[spec.Bucket+"/"+spec.Object]; !ok {
		return nil, fmt.Errorf("no source gs://%s/%s", spec.Bucket, spec.Object)
	}
	for match, msg := range c.failBuild {
		if strings.HasPrefix(spec.Image, match) {
			return nil, errors.New(msg)
		}
	}
	sum := sha256.Sum256([]byte(spec.Image + "@" + spec.Object))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	c.images[spec.Image] = digest
	c.change("push %s", spec.Image)
	return &gcp.BuildResult{ID: "build-" + hex.EncodeToString(sum[:4]), LogURL: "https://console.cloud.google.com/cloud-build/builds/x", Digest: digest}, nil
}

func (c *fakeCloud) EnsureJob(_ context.Context, project, region string, spec gcp.JobSpec) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := project + "/" + region + "/" + spec.Name
	if prev, ok := c.jobs[key]; ok && prev.Image == spec.Image && prev.ServiceAccount == spec.ServiceAccount && prev.Timeout == spec.Timeout {
		return false, nil
	}
	c.jobs[key] = spec
	c.change("job %s runs %s", key, spec.Image)
	return true, nil
}

func (c *fakeCloud) JobImage(_ context.Context, project, region, job string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := project + "/" + region + "/" + job
	if image, ok := c.graphJobs[key]; ok {
		return image, nil
	}
	if spec, ok := c.jobs[key]; ok {
		return spec.Image, nil
	}
	return "", fmt.Errorf("job %s: %w", key, fs.ErrNotExist)
}

func (c *fakeCloud) RunJob(_ context.Context, project, region, job string, args []string) (*gcp.JobRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := project + "/" + region + "/" + job
	_, ensured := c.jobs[key]
	_, graph := c.graphJobs[key]
	if !ensured && !graph {
		return nil, fmt.Errorf("no job %s", key)
	}
	c.runs = append(c.runs, slices.Clone(args))
	name := fmt.Sprintf("%s/executions/%d", key, len(c.runs))
	subject := strings.Join(args, " ")
	if args == nil {
		subject = job
		c.record("cloud run %s", job)
	} else {
		c.record("cloud run job %s", subject)
	}
	if err := c.failWait; err != nil {
		c.failWait = nil
		return nil, fmt.Errorf("gcp: job %s: waiting for execution %s, which may still be running: %w", key, name, err)
	}
	for match, fail := range c.failRun {
		if strings.Contains(subject, match) {
			delete(c.failRun, match)
			c.stderr[name] = fail.stderr
			return &gcp.JobRun{Name: name, LogURI: "https://console.cloud.google.com/logs/x", Message: fail.message}, nil
		}
	}
	return &gcp.JobRun{Name: name, LogURI: "https://console.cloud.google.com/logs/x", Succeeded: true}, nil
}

func (c *fakeCloud) ExecutionStderr(_ context.Context, run *gcp.JobRun, match string, wait time.Duration) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stderrReads = append(c.stderrReads, stderrRead{run.Name, match, wait})
	if c.failStderr != nil {
		return nil, c.failStderr
	}
	return slices.Clone(c.stderr[run.Name]), nil
}

func (c *fakeCloud) change(format string, args ...any) {
	c.changes = append(c.changes, fmt.Sprintf(format, args...))
}

func (c *fakeCloud) Changes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.changes)
}

func (c *fakeCloud) EnableServices(_ context.Context, project string, services []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range services {
		if !slices.Contains(c.enabled[project], s) {
			c.enabled[project] = append(c.enabled[project], s)
			c.change("enable %s", s)
		}
	}
	return nil
}

func (c *fakeCloud) ProjectNumber(_ context.Context, project string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	number, ok := c.numbers[project]
	if !ok {
		return "", fmt.Errorf("project %s does not exist", project)
	}
	return number, nil
}

func (c *fakeCloud) EnsureBucket(_ context.Context, project, bucket, location string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.buckets[bucket]; ok {
		return false, nil
	}
	c.buckets[bucket] = location
	c.change("create bucket %s in %s/%s", bucket, project, location)
	return true, nil
}

func (c *fakeCloud) EnsureKey(_ context.Context, project, location, keyRing, key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name := strings.Join([]string{project, location, keyRing, key}, "/")
	if c.keys[name] {
		return false, nil
	}
	c.keys[name] = true
	c.change("create key %s", name)
	return true, nil
}

func (c *fakeCloud) ReadObject(_ context.Context, bucket, object string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.objects[bucket+"/"+object]
	if !ok {
		return nil, fmt.Errorf("%s/%s: %w", bucket, object, fs.ErrNotExist)
	}
	return slices.Clone(data), nil
}

func (c *fakeCloud) WriteObject(_ context.Context, bucket, object string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[bucket+"/"+object] = slices.Clone(data)
	return nil
}

func (c *fakeCloud) WriteSiteObject(_ context.Context, bucket, object string, data []byte, contentType, cacheControl string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.absent[bucket] {
		return fmt.Errorf("gs://%s: %w", bucket, fs.ErrNotExist)
	}
	c.objects[bucket+"/"+object] = slices.Clone(data)
	c.served[bucket+"/"+object] = [2]string{contentType, cacheControl}
	if strings.Contains(object, "/__superschematic/") {
		c.record("write gs://%s/%s", bucket, object)
	}
	return nil
}

func (c *fakeCloud) DeleteObject(_ context.Context, bucket, object string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, bucket+"/"+object)
	return nil
}

func (c *fakeCloud) EnsureSecret(_ context.Context, project, secret string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.secrets[project+"/"+secret]; ok {
		return false, nil
	}
	c.secrets[project+"/"+secret] = &fakeSecret{}
	c.change("create secret %s/%s", project, secret)
	return true, nil
}

func (c *fakeCloud) GrantSecretAccess(_ context.Context, project, secret string, members []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.secrets[project+"/"+secret]
	if !ok {
		return fmt.Errorf("no secret %s/%s", project, secret)
	}
	for _, m := range members {
		if !slices.Contains(s.accessors, m) {
			s.accessors = append(s.accessors, m)
			c.change("grant %s on %s/%s", m, project, secret)
		}
	}
	return nil
}

func (c *fakeCloud) SecretHasValue(_ context.Context, project, secret string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.secrets[project+"/"+secret]
	return ok && len(s.versions) > 0, nil
}

func (c *fakeCloud) AddSecretVersion(_ context.Context, project, secret string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.secrets[project+"/"+secret]
	if !ok {
		return fmt.Errorf("secret %s/%s: %w", project, secret, registry.ErrSecretNotCreated)
	}
	s.versions = append(s.versions, slices.Clone(value))
	return nil
}

func (c *fakeCloud) AccessSecret(_ context.Context, project, secret string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.secrets[project+"/"+secret]
	if !ok || len(s.versions) == 0 {
		return nil, fmt.Errorf("secret %s/%s has no version", project, secret)
	}
	return slices.Clone(s.versions[len(s.versions)-1]), nil
}

// fakeRunner records each migration phase in the provisioner's call log.
type fakeRunner struct{ log *stacktest.FakeProvisioner }

func (r fakeRunner) Migrate(_ context.Context, req registry.MigrationRequest) error {
	var services []string
	for _, p := range req.Plans {
		services = append(services, p.Service)
	}
	r.log.Record("migrate %s %s: %s", req.Phase, req.Database, strings.Join(services, ", "))
	return nil
}

// deployFixture assembles the gcp target over a fake cloud, with the fake
// provisioner under the name the target names.
type deployFixture struct {
	reg   *registry.Registry
	cloud *fakeCloud
	prov  *stacktest.FakeProvisioner
}

type provisionerStub struct{ prov *stacktest.FakeProvisioner }

func (provisionerStub) Name() string { return "pulumi-stub" }

func (s provisionerStub) Register(r *registry.Registry) error {
	return r.RegisterProvisioner(registry.ProvisionerSpec{Name: gcp.Provisioner, Extension: "pulumi-stub", Provisioner: s.prov})
}

func newDeployFixture(t *testing.T, withRunner bool) *deployFixture {
	t.Helper()
	f := &deployFixture{cloud: newFakeCloud(), prov: &stacktest.FakeProvisioner{}}
	ext := gcp.Extension{Cloud: f.cloud}
	if withRunner {
		ext.Migrations = fakeRunner{log: f.prov}
	}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext, provisionerStub{prov: f.prov})
	if err != nil {
		t.Fatal(err)
	}
	f.reg = reg
	return f
}

// shopImages are images of the shop's servers, its job and its worker in
// the repository the Cloud Run platforms write.
func shopImages(project string, n int) map[string]string {
	digest := "sha256:" + strings.Repeat(fmt.Sprintf("%x", n), 64)
	repo := "us-east1-docker.pkg.dev/" + project + "/shop/"
	return map[string]string{
		"shop-api":                  repo + "shop-api@" + digest,
		"Orders":                    repo + "orders@" + digest,
		"shop-orders-ship-orders":   repo + "shop-orders-ship-orders@" + digest,
		"shop-orders-fulfil-orders": repo + "shop-orders-fulfil-orders@" + digest,
	}
}

// shopPlanner plans shop-db with a step in each phase.
func shopPlanner(service, dialect string, _ json.RawMessage) (*stack.DatabasePlan, error) {
	return &stack.DatabasePlan{
		Service: service, Dialect: dialect,
		To: "v1", ToModel: json.RawMessage(`{"v":1}`),
		Expanded: "v0", ExpandedModel: json.RawMessage(`{"v":0}`),
		ExpandSteps: 2, ContractSteps: 1,
		Hash: "plan", Document: json.RawMessage(`{"plan":1}`),
	}, nil
}

// TestDeployShopOnGCP deploys the golden Staging environment on gcp with
// fakes: the steps run in deploy order, the images are pinned in the
// Cloud Run services, the secret comes from Secret Manager, and the
// manifest lands in the state bucket.
func TestDeployShopOnGCP(t *testing.T) {
	f := newDeployFixture(t, true)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	ctx := context.Background()
	// The infrastructure step creates the secret; the fake provisioner
	// creates nothing, so the test does.
	if _, err := f.cloud.EnsureSecret(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := f.cloud.AddSecretVersion(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY", []byte("sk")); err != nil {
		t.Fatal(err)
	}
	m, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Images:  shopImages("acme-staging", 1),
		Planner: shopPlanner,
	})
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, call := range f.prov.Calls() {
		steps = append(steps, strings.SplitN(call, ":", 2)[0])
	}
	want := []string{"render 56 nodes", "apply infrastructure", "migrate expand shop-db", "apply rollout 1", "apply rollout 2", "migrate contract shop-db", "apply exposure"}
	if !slices.Equal(steps, want) {
		t.Errorf("ran %q, want %q", steps, want)
	}
	service := f.prov.Rendered().Resources.Resource("Orders.service")
	containers := service.Properties["template"].(map[string]any)["containers"].([]any)
	if got := containers[0].(map[string]any)["image"]; got != shopImages("acme-staging", 1)["Orders"] {
		t.Errorf("Orders.service runs %v", got)
	}
	data, err := f.cloud.ReadObject(ctx, "acme-staging-superschematic-state", "superschematic/manifests/shop/Staging.json")
	if err != nil {
		t.Fatal(err)
	}
	back, err := stack.UnmarshalManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != stack.StatusDeployed || back.Databases["shop-db"]["shop-db"].Hash != "v1" || m.Run != "Staging" {
		t.Errorf("manifest: %s, %+v", back.Status, back.Databases)
	}
}

// TestStores covers the state bucket and Secret Manager names.
func TestStores(t *testing.T) {
	f := newDeployFixture(t, false)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Preview")
	target, _ := f.reg.Target(gcp.Target)
	ctx := context.Background()
	backend, err := target.State.Backend(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if backend.URL != "gs://acme-staging-superschematic-state" ||
		backend.SecretsProvider != "gcpkms://projects/acme-staging/locations/us-east1/keyRings/superschematic/cryptoKeys/pulumi-state" {
		t.Errorf("backend %+v", backend)
	}
	run := registry.Run{Environment: env, Parameters: map[string]string{"pr": "12"}}
	if _, err := target.State.ReadManifest(ctx, run); !errors.Is(err, registry.ErrNoManifest) {
		t.Errorf("ReadManifest before a deploy: %v", err)
	}
	if err := target.State.WriteManifest(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cloud.ReadObject(ctx, "acme-staging-superschematic-state", "superschematic/manifests/shop/Preview.pr-12.json"); err != nil {
		t.Errorf("the manifest is not where expected: %v", err)
	}
	if err := target.State.DeleteManifest(ctx, run); err != nil {
		t.Fatal(err)
	}

	// An application secret is named after the stack, its type and
	// field, and lives in the member's project, its parent's; a
	// credential is named as it is.
	secret := "PaymentsSecrets.STRIPE_KEY"
	if err := target.Secrets.Set(ctx, env, secret, []byte("x")); !errors.Is(err, registry.ErrSecretNotCreated) {
		t.Errorf("Set before the secret exists: %v", err)
	}
	for _, name := range []string{"Shop-PaymentsSecrets-STRIPE_KEY", "shop-cloudflare-dns-acme_dev"} {
		if _, err := f.cloud.EnsureSecret(ctx, "acme-staging", name); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := target.Secrets.Exists(ctx, env, secret); ok {
		t.Error("a secret with no version exists")
	}
	if err := target.Secrets.Set(ctx, env, secret, []byte("sk_1")); err != nil {
		t.Fatal(err)
	}
	if err := target.Secrets.Set(ctx, env, "shop-cloudflare-dns-acme_dev", []byte("cf")); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.cloud.AccessSecret(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY"); string(got) != "sk_1" {
		t.Errorf("Secret Manager holds %q", got)
	}
	if got, _ := target.Secrets.Get(ctx, env, "shop-cloudflare-dns-acme_dev"); string(got) != "cf" {
		t.Errorf("Get = %q", got)
	}
	if ids, _ := target.Secrets.List(ctx, env); !slices.Equal(ids, []string{secret}) {
		t.Errorf("List = %v", ids)
	}
}

// TestBootstrap bootstraps Staging's project twice: the first run enables
// the APIs, Cloud Scheduler's among them for the job's schedule (D52),
// creates the state bucket and key, applies the bootstrap graph
// and creates each credential's secret, and returns the project's number to
// record beside project (D47); the second changes nothing, applies the
// same graph and returns the same number.
func TestBootstrap(t *testing.T) {
	f := newDeployFixture(t, false)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	target, _ := f.reg.Target(gcp.Target)
	ctx := context.Background()
	cred := registry.Credential{Secret: "shop-cloudflare-dns-acme_dev", Env: "CLOUDFLARE_API_TOKEN", Description: "A token"}
	req := registry.BootstrapRequest{
		Environment: env, Repository: "acme/shop",
		Credentials: []registry.Credential{cred, cred},
		Provisioner: f.prov, Dir: t.TempDir(),
	}
	result, err := target.Bootstrap.Bootstrap(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	wantNumber := []registry.BootstrapValue{{Key: "projectNumber", Value: "123456789012", Beside: "project"}}
	if result == nil || !slices.Equal(result.Values, wantNumber) {
		t.Errorf("bootstrap returned %+v, want %+v", result, wantNumber)
	}
	first := f.cloud.Changes()
	for _, want := range []string{
		"enable compute.googleapis.com", "enable run.googleapis.com", "enable sqladmin.googleapis.com",
		"enable certificatemanager.googleapis.com", "enable dns.googleapis.com", "enable sts.googleapis.com",
		"enable cloudscheduler.googleapis.com",
		"create bucket acme-staging-superschematic-state in acme-staging/us-east1",
		"create key acme-staging/us-east1/superschematic/pulumi-state",
		"create secret acme-staging/shop-cloudflare-dns-acme_dev",
		"grant serviceAccount:shop-deployer@acme-staging.iam.gserviceaccount.com on acme-staging/shop-cloudflare-dns-acme_dev",
		"grant serviceAccount:shop-planner@acme-staging.iam.gserviceaccount.com on acme-staging/shop-cloudflare-dns-acme_dev",
	} {
		if !slices.Contains(first, want) {
			t.Errorf("the first bootstrap did not %s:\n%s", want, strings.Join(first, "\n"))
		}
	}
	if n := strings.Count(strings.Join(first, "\n"), "create secret"); n != 1 {
		t.Errorf("created %d secrets for one credential named twice", n)
	}
	graph := f.prov.Rendered()
	applied := f.prov.Calls()

	result, err = target.Bootstrap.Bootstrap(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !slices.Equal(result.Values, wantNumber) {
		t.Errorf("the second bootstrap returned %+v, want %+v", result, wantNumber)
	}
	if again := f.cloud.Changes(); len(again) != len(first) {
		t.Errorf("the second bootstrap changed %v", again[len(first):])
	}
	a, _ := stack.Marshal(graph)
	b, _ := stack.Marshal(f.prov.Rendered())
	if !bytes.Equal(a, b) {
		t.Error("the second bootstrap rendered another graph")
	}
	if calls := f.prov.Calls(); !slices.Equal(calls[len(applied):], applied) {
		t.Errorf("the second bootstrap ran %v, want %v again", calls[len(applied):], applied)
	}
}

// TestBootstrapGraph checks the bootstrap graph against the golden file,
// validates every node against the pinned schema of its type, and checks
// what a graph without a repository leaves out.
func TestBootstrapGraph(t *testing.T) {
	reg := assemble(t)
	env := resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	graph, err := gcp.BootstrapEnvironment(env, "acme/shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range graph.Resources.Resources {
		ok, err := reg.ValidateResource(res)
		if !ok || err != nil {
			t.Errorf("node %s (%s): pinned %v, %v", res.ID, res.Type, ok, err)
		}
	}
	got, err := stack.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "golden", "bootstrap", "Staging.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the bootstrap graph differs from %s; run go test -update and review the diff", path)
	}

	bare, err := gcp.BootstrapEnvironment(env, "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, res := range bare.Resources.Resources {
		if strings.Contains(res.ID, "github") {
			ids = append(ids, res.ID)
		}
	}
	if len(ids) > 0 {
		t.Errorf("a graph without a repository has %v", ids)
	}

	long := *env
	long.Stack = "AVeryLongStackNameForAnAccount"
	if _, err := gcp.BootstrapEnvironment(&long, "acme/shop"); err == nil || !strings.Contains(err.Error(), "GCP allows 30") {
		t.Errorf("a stack name too long for an account id: %v", err)
	}
}

// TestBootstrapGraphNoDeprecatedProperties walks the bootstrap graph as
// TestNoDeprecatedProperties walks the environments'.
func TestBootstrapGraphNoDeprecatedProperties(t *testing.T) {
	reg := assemble(t)
	env := resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	graph, err := gcp.BootstrapEnvironment(env, "acme/shop")
	if err != nil {
		t.Fatal(err)
	}
	checkNoDeprecated(t, "bootstrap", []*ir.ResolvedEnvironment{graph})
}
