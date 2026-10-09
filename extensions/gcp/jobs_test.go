package gcp_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// The state bucket, the repository and the accounts of the shop's
// Staging on gcp.
const (
	stateBucket = "acme-staging-superschematic-state"
	shopRepo    = "us-east1-docker.pkg.dev/acme-staging/shop/"
	migrateRepo = shopRepo + "superschematic-migrate"
	jobKey      = "acme-staging/us-east1/shop-migrate"
)

// newJobFixture assembles the gcp target over a fake cloud that records
// its builds and job runs in the provisioner's call log, with no fake
// migration runner: migrations run as the Cloud Run job.
func newJobFixture(t *testing.T, ext gcp.Extension) *deployFixture {
	t.Helper()
	t.Setenv(gcp.MigrateImageEnv, "")
	f := &deployFixture{cloud: newFakeCloud(), prov: &stacktest.FakeProvisioner{}}
	f.cloud.log = f.prov
	ext.Cloud = f.cloud
	reg, err := registry.Assemble(registry.DefaultNaming(), ext, provisionerStub{prov: f.prov})
	if err != nil {
		t.Fatal(err)
	}
	f.reg = reg
	return f
}

// ready stores the shop's secret, which the infrastructure step would
// create.
func (f *deployFixture) ready(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.cloud.EnsureSecret(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := f.cloud.AddSecretVersion(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY", []byte("sk")); err != nil {
		t.Fatal(err)
	}
}

// shopSources writes a repository with the Dockerfile the shop's build
// writes for each of its servers, its job and its worker.
func shopSources(t *testing.T) *stack.Sources {
	t.Helper()
	root := t.TempDir()
	src := &stack.Sources{OutputRoot: filepath.Join(root, "schemas", "dist"), RepositoryRoot: root}
	for _, server := range []string{"shop-api", "Orders", "shop-orders-ship-orders", "shop-orders-fulfil-orders"} {
		dockerfile := src.Dockerfile("Shop", server)
		for path, content := range map[string]string{
			dockerfile:                   "FROM scratch\n",
			dockerfile + ".dockerignore": "*\n!go/" + server + "\n!schemas/dist/server/Shop/" + server + "\n",
			filepath.Join(root, "go", server, "implementation.go"): "package impl\n",
			filepath.Join(root, "secrets.env"):                     "KEY=value\n",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return src
}

// firstPlanner plans shop-db with a step in each phase from an empty
// database, and nothing to do from the schema it ends at.
func firstPlanner(service, dialect string, from json.RawMessage) (*stack.DatabasePlan, error) {
	if len(from) > 0 {
		return &stack.DatabasePlan{Service: service, Dialect: dialect, From: "v1", To: "v1", ToModel: json.RawMessage(`{"v":1}`), Hash: "none"}, nil
	}
	return shopPlanner(service, dialect, from)
}

// steps reads the call log with each digest and object hash shortened,
// so a test reads the order calls ran in.
var hexRun = regexp.MustCompile(`[0-9a-f]{16,64}`)

func steps(calls []string) []string {
	var out []string
	for _, c := range calls {
		c = hexRun.ReplaceAllString(c, "<hex>")
		if !strings.HasPrefix(c, "cloud ") {
			c = strings.SplitN(c, ":", 2)[0]
		}
		out = append(out, c)
	}
	return out
}

// archiveFiles reads a gzipped tarball's files.
func archiveFiles(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[hdr.Name] = string(body)
	}
}

// object reads an object of the fake state bucket.
func (f *deployFixture) object(t *testing.T, name string) []byte {
	t.Helper()
	data, err := f.cloud.ReadObject(context.Background(), stateBucket, name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// jobDocument reads the job document of a run's arguments.
func (f *deployFixture) jobDocument(t *testing.T, args []string) (string, map[string]any) {
	t.Helper()
	if len(args) != 3 || args[0] != "job" || args[1] != "--job" || !strings.HasPrefix(args[2], "gs://"+stateBucket+"/") {
		t.Fatalf("the job ran with %q", args)
	}
	name := strings.TrimPrefix(args[2], "gs://"+stateBucket+"/")
	data := f.object(t, name)
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return name, doc
}

// TestDeployBuildsAndMigratesOnGCP deploys Staging on gcp with fakes and
// no image: the deploy builds each server's and the job's image with Cloud
// Build before it changes anything, pins the digests it pushed, and runs
// each migration phase as an execution of the stack's Cloud Run job, whose
// image it builds from the release once. A deploy with nothing changed
// builds and runs nothing.
func TestDeployBuildsAndMigratesOnGCP(t *testing.T) {
	f := newJobFixture(t, gcp.Extension{MigrateVersion: "1.2.3"})
	f.ready(t)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	ctx := context.Background()
	o := stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Sources: shopSources(t),
		Planner: firstPlanner,
	}
	m, err := stack.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cloud build " + shopRepo + "orders:context-<hex>",
		"cloud build " + shopRepo + "shop-api:context-<hex>",
		"cloud build " + shopRepo + "shop-orders-fulfil-orders:context-<hex>",
		"cloud build " + shopRepo + "shop-orders-ship-orders:context-<hex>",
		"render 56 nodes",
		"apply infrastructure",
		"cloud build " + migrateRepo + ":1.2.3",
		"cloud run job job --job gs://" + stateBucket + "/superschematic/migrations/shop/Staging/shop-db/expand-<hex>.json",
		"apply rollout 1",
		"apply rollout 2",
		"cloud run job job --job gs://" + stateBucket + "/superschematic/migrations/shop/Staging/shop-db/contract-<hex>.json",
		"apply exposure",
	}
	if got := steps(f.prov.Calls()); !slices.Equal(got, want) {
		t.Fatalf("the deploy ran:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The servers' builds: their contexts, by the builder account.
	build := f.cloud.builds[0]
	if build.Project != "acme-staging" || build.Region != "us-east1" || build.Bucket != stateBucket ||
		build.Dockerfile != "schemas/dist/server/Shop/Orders/Dockerfile" ||
		build.ServiceAccount != "shop-builder@acme-staging.iam.gserviceaccount.com" ||
		!regexp.MustCompile(`^superschematic/builds/shop/orders/[0-9a-f]{64}\.tar\.gz$`).MatchString(build.Object) {
		t.Errorf("the build of Orders: %+v", build)
	}
	var names []string
	for name := range archiveFiles(t, f.object(t, build.Object)) {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"go/Orders/", "go/Orders/implementation.go", "schemas/dist/server/Shop/Orders/",
		"schemas/dist/server/Shop/Orders/Dockerfile", "schemas/dist/server/Shop/Orders/Dockerfile.dockerignore"}; !slices.Equal(names, want) {
		t.Errorf("Orders' context holds %q, want %q", names, want)
	}
	orders := m.Images["Orders"]
	if !strings.HasPrefix(orders, shopRepo+"orders@sha256:") || !strings.HasSuffix(build.Object, strings.TrimPrefix(m.Contexts["Orders"], "sha256:")+".tar.gz") {
		t.Errorf("manifest: Orders runs %s built from %s", orders, m.Contexts["Orders"])
	}
	containers := f.prov.Rendered().Resources.Resource("Orders.service").Properties["template"].(map[string]any)["containers"].([]any)
	if got := containers[0].(map[string]any)["image"]; got != orders {
		t.Errorf("Orders.service runs %v, want the image built, %s", got, orders)
	}

	// The job's image, pinned in its Cloud Run job as a server's is in its
	// service (D52).
	job := m.Images["shop-orders-ship-orders"]
	task := f.prov.Rendered().Resources.Resource("shop-orders-ship-orders.job").Properties["template"].(map[string]any)["template"].(map[string]any)
	if got := task["containers"].([]any)[0].(map[string]any)["image"]; !strings.HasPrefix(job, shopRepo+"shop-orders-ship-orders@sha256:") || got != job {
		t.Errorf("shop-orders-ship-orders.job runs %v, want the image built, %s", got, job)
	}

	// The worker's image, pinned in its worker pool (D53).
	worker := m.Images["shop-orders-fulfil-orders"]
	pool := f.prov.Rendered().Resources.Resource("shop-orders-fulfil-orders.worker-pool").Properties["template"].(map[string]any)
	if got := pool["containers"].([]any)[0].(map[string]any)["image"]; !strings.HasPrefix(worker, shopRepo+"shop-orders-fulfil-orders@sha256:") || got != worker {
		t.Errorf("shop-orders-fulfil-orders.worker-pool runs %v, want the image built, %s", got, worker)
	}

	// The runner's image, from the release's module.
	runner := f.cloud.builds[4]
	if runner.Image != migrateRepo+":1.2.3" || runner.Dockerfile != "Dockerfile" || runner.Object != "superschematic/builds/shop/superschematic-migrate/1.2.3.tar.gz" {
		t.Errorf("the runner's build: %+v", runner)
	}
	dockerfile := archiveFiles(t, f.object(t, runner.Object))["Dockerfile"]
	for _, line := range []string{
		"ARG GO_VERSION=" + stack.GoVersion,
		"RUN go install github.com/parable-work/superschematic/runtime/migrate/go/cmd/superschematic-migrate@v1.2.3",
		"FROM gcr.io/distroless/static-debian13:nonroot",
		`ENTRYPOINT ["/superschematic-migrate"]`,
	} {
		if !strings.Contains(dockerfile, line+"\n") {
			t.Errorf("the runner's Dockerfile lacks %q:\n%s", line, dockerfile)
		}
	}

	// The migration job: the runner's image, as the migrator.
	migrate := f.cloud.jobs[jobKey]
	if migrate.Image != migrateRepo+"@"+f.cloud.images[migrateRepo+":1.2.3"] || migrate.Container != "migrate" ||
		migrate.ServiceAccount != "shop-migrator@acme-staging.iam.gserviceaccount.com" || migrate.Timeout != time.Hour {
		t.Errorf("job %+v", migrate)
	}

	// The expand phase's job document, checked in as a golden the runner's
	// tests read too, and the plan beside it.
	name, doc := f.jobDocument(t, f.cloud.runs[0])
	data := f.object(t, name)
	golden := filepath.Join("testdata", "golden", "migrations", "Staging-expand.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if wantDoc, err := os.ReadFile(golden); err != nil || !bytes.Equal(data, wantDoc) {
		t.Errorf("the job document differs from %s (run go test -update and review the diff): %v\n%s", golden, err, data)
	}
	plan := doc["databases"].([]any)[0].(map[string]any)["plan"].(string)
	if got := f.object(t, strings.TrimPrefix(plan, "gs://"+stateBucket+"/")); string(got) != `{"plan":1}` {
		t.Errorf("the plan in the bucket is %s", got)
	}
	if !strings.Contains(string(f.object(t, "superschematic/manifests/shop/Staging.json")), `"servers": [`) {
		t.Error("the manifest does not record the servers the job gave privileges")
	}

	// Nothing changed: no build, no job.
	n := len(f.prov.Calls())
	if _, err := stack.Deploy(ctx, o); err != nil {
		t.Fatal(err)
	}
	if got := steps(f.prov.Calls()[n:]); slices.ContainsFunc(got, func(c string) bool { return strings.HasPrefix(c, "cloud ") }) {
		t.Errorf("a deploy with nothing changed ran:\n%s", strings.Join(got, "\n"))
	}
}

// TestMigrationJobGrantsAndRecovers: a deploy whose plan has no steps runs
// the job for the servers and jobs that connect when they changed, with no
// plan in the document; an execution that fails stops the deploy with the
// runner's error and its logs, and the next deploy runs the phase again. A
// job of the graph connects by its own IAM database user (D52).
func TestMigrationJobGrantsAndRecovers(t *testing.T) {
	f := newJobFixture(t, gcp.Extension{MigrateVersion: "1.2.3"})
	f.ready(t)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	ctx := context.Background()
	o := stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Images:  shopImages("acme-staging", 1),
		Planner: firstPlanner,
	}
	f.cloud.failRun["expand"] = fakeFailure{message: cloudRunExited, stderr: []string{"superschematic-migrate: service shop-db: the task exited 1"}}
	m, err := stack.Deploy(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "gcp: the expand phase on shop-db failed in job shop-migrate, execution "+jobKey+"/executions/1: superschematic-migrate: service shop-db: the task exited 1 (logs: https://") {
		t.Fatalf("deploy = %v", err)
	}
	if applied := m.Databases["shop-db"]["shop-db"]; applied.Pending == nil || applied.Pending.Phase != ir.MigrationExpand {
		t.Fatalf("shop-db: %+v", applied)
	}
	if slices.ContainsFunc(f.prov.Calls(), func(c string) bool { return strings.HasPrefix(c, "apply rollout") }) {
		t.Error("the deploy rolled out after the expand phase failed")
	}

	// The next deploy runs the pending phase first, then plans from where
	// it ends, which has nothing left to do here.
	n := len(f.cloud.runs)
	m, err = stack.Deploy(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if runs := len(f.cloud.runs) - n; runs != 1 || m.Status != stack.StatusDeployed {
		t.Errorf("the next deploy ran the job %d time(s), status %s", runs, m.Status)
	}
	if name, _ := f.jobDocument(t, f.cloud.runs[n]); !strings.Contains(name, "/shop-db/expand-") {
		t.Errorf("the next deploy ran %s first", name)
	}
	if got := m.Databases["shop-db"]["shop-db"].Servers; !slices.Equal(got, []string{"Orders", "shop-api", "shop-orders-fulfil-orders", "shop-orders-ship-orders"}) {
		t.Errorf("the manifest records servers %v", got)
	}

	// A manifest from before Orders and the job connected: the migration
	// job runs for the privileges alone.
	m.Databases["shop-db"]["shop-db"].Servers = []string{"shop-api"}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cloud.WriteObject(ctx, stateBucket, "superschematic/manifests/shop/Staging.json", data); err != nil {
		t.Fatal(err)
	}
	n = len(f.cloud.runs)
	if _, err := stack.Deploy(ctx, o); err != nil {
		t.Fatal(err)
	}
	if len(f.cloud.runs) != n+1 {
		t.Fatalf("the job ran %d time(s), want once", len(f.cloud.runs)-n)
	}
	_, doc := f.jobDocument(t, f.cloud.runs[n])
	got, _ := json.Marshal(doc["databases"])
	if want := `[{"database":"shop_db","privileges":{"readWrite":["orders@acme-staging.iam","shop-api@acme-staging.iam","shop-orders-fulfil-orders@acme-staging.iam","shop-orders-ship-orders@acme-staging.iam"]},"service":"shop-db"}]`; string(got) != want {
		t.Errorf("the job's databases: %s\nwant %s", got, want)
	}
}

// cloudRunExited is all Cloud Run says of a task that exited 1.
const cloudRunExited = "Task shop-migrate-x7k2p-task0 failed with exit code: 1 and message: The container exited with an error."

// TestMigrationJobReportsTheRunnerError: a failed execution's error is the
// runner's, from what its task wrote to stderr, which the deploy reads
// once Cloud Run says it failed: the last line that begins with the
// runner's name, else the first line, as a panic writes; Cloud Run's
// message, saying why, when there is none or it cannot be read. A wait that fails is no failed execution: it
// reads nothing, the phase stays pending, and the next deploy runs it.
func TestMigrationJobReportsTheRunnerError(t *testing.T) {
	const logs = "(logs: https://console.cloud.google.com/logs/x"
	for _, c := range []struct {
		name       string
		stderr     []string
		failStderr error
		failWait   error
		want       string
	}{{
		name: "the runner's error",
		stderr: []string{
			"2026/10/07 18:02:11 cloudsqlconn: refreshing the certificate of acme-staging:us-east1:shop-db",
			"superschematic-migrate: read gs://" + stateBucket + "/superschematic/migrations/shop/Staging/shop-db/expand-0123.json: file does not exist",
		},
		want: "execution " + jobKey + "/executions/1: superschematic-migrate: read gs://" + stateBucket + "/superschematic/migrations/shop/Staging/shop-db/expand-0123.json: file does not exist " + logs + ")",
	}, {
		name: "a step's error",
		stderr: []string{
			`superschematic-migrate: service shop-db: step 2 (order.note) failed: ERROR: column "note" of relation "order" already exists (SQLSTATE 42701)`,
			"statement:",
			`ALTER TABLE "order" ADD COLUMN "note" text`,
		},
		want: `/executions/1: superschematic-migrate: service shop-db: step 2 (order.note) failed: ERROR: column "note" of relation "order" already exists (SQLSTATE 42701) ` + logs + ")",
	}, {
		name: "a usage error",
		stderr: []string{
			"flag provided but not defined: -jb",
			"Usage of superschematic-migrate job:",
			"superschematic-migrate: flag provided but not defined: -jb",
			"usage:",
			"superschematic-migrate apply --plan plan.json [--phase expand|contract|all] [--database-url URL]",
		},
		want: "/executions/1: superschematic-migrate: flag provided but not defined: -jb " + logs + ")",
	}, {
		name: "a panic",
		stderr: []string{
			"panic: runtime error: invalid memory address or nil pointer dereference",
			"[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x5d1a2c]",
			"goroutine 1 [running]:",
		},
		want: "/executions/1: panic: runtime error: invalid memory address or nil pointer dereference " + logs + ")",
	}, {
		name: "no stderr",
		want: "/executions/1: " + cloudRunExited + " " + logs + "; Cloud Logging held no stderr of it after 30s)",
	}, {
		name:       "stderr unread",
		stderr:     []string{"superschematic-migrate: never read"},
		failStderr: errors.New("rpc error: code = PermissionDenied desc = Permission denied for all log views"),
		want:       "/executions/1: " + cloudRunExited + " " + logs + "; its stderr is unread: rpc error: code = PermissionDenied desc = Permission denied for all log views)",
	}, {
		name:     "the wait fails",
		failWait: errors.New("rpc error: code = Unavailable desc = connection reset by peer"),
		want:     "gcp: job " + jobKey + ": waiting for execution " + jobKey + "/executions/1, which may still be running: rpc error: code = Unavailable desc = connection reset by peer",
	}} {
		t.Run(c.name, func(t *testing.T) {
			f := newJobFixture(t, gcp.Extension{MigrateImage: migrateRepo + "@sha256:" + strings.Repeat("c", 64)})
			f.ready(t)
			env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
			ctx := context.Background()
			o := stack.DeployOptions{
				Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
				Images:  shopImages("acme-staging", 1),
				Planner: firstPlanner,
			}
			f.cloud.failRun["expand"] = fakeFailure{message: cloudRunExited, stderr: c.stderr}
			f.cloud.failWait, f.cloud.failStderr = c.failWait, c.failStderr
			m, err := stack.Deploy(ctx, o)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("deploy = %v\nwant it to hold %s", err, c.want)
			}
			if applied := m.Databases["shop-db"]["shop-db"]; applied.Pending == nil || applied.Pending.Phase != ir.MigrationExpand {
				t.Fatalf("shop-db: %+v", applied)
			}
			if c.failWait != nil {
				if strings.Contains(err.Error(), "failed in job") || len(f.cloud.stderrReads) > 0 {
					t.Errorf("a wait that failed is read as a failed execution: %v, %d stderr read(s)", err, len(f.cloud.stderrReads))
				}
				f.cloud.failRun = map[string]fakeFailure{}
				if _, err := stack.Deploy(ctx, o); err != nil {
					t.Fatal(err)
				}
				if name, _ := f.jobDocument(t, f.cloud.runs[1]); !strings.Contains(name, "/shop-db/expand-") {
					t.Errorf("the next deploy ran %s first", name)
				}
				return
			}
			want := []stderrRead{{execution: jobKey + "/executions/1", wait: 30 * time.Second}}
			if !slices.Equal(f.cloud.stderrReads, want) {
				t.Errorf("stderr reads %+v, want %+v", f.cloud.stderrReads, want)
			}
		})
	}
}

// TestMigrationJobOfAMember: a run of the parameterized Preview migrates
// its own database on the parent's instance, for its own servers' and
// job's users.
func TestMigrationJobOfAMember(t *testing.T) {
	f := newJobFixture(t, gcp.Extension{MigrateImage: migrateRepo + "@sha256:" + strings.Repeat("c", 64)})
	f.ready(t)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Preview")
	_, err := stack.Deploy(context.Background(), stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env, Parameters: map[string]string{"pr": "7"}}, Dir: t.TempDir()},
		Images:  shopImages("acme-staging", 1),
		Planner: firstPlanner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.cloud.builds) > 0 {
		t.Errorf("a named runner image was built: %+v", f.cloud.builds)
	}
	if job := f.cloud.jobs[jobKey]; job.Image != migrateRepo+"@sha256:"+strings.Repeat("c", 64) {
		t.Errorf("the job runs %s", job.Image)
	}
	name, doc := f.jobDocument(t, f.cloud.runs[0])
	if !strings.HasPrefix(name, "superschematic/migrations/shop/Preview.pr-7/shop-db/expand-") {
		t.Errorf("the job document is at %s", name)
	}
	got, _ := json.Marshal(map[string]any{"cloudSql": doc["cloudSql"], "privileges": doc["databases"].([]any)[0].(map[string]any)["privileges"],
		"database": doc["databases"].([]any)[0].(map[string]any)["database"]})
	want := `{"cloudSql":{"instance":"acme-staging:us-east1:shop-db","user":"shop-migrator@acme-staging.iam"},"database":"shop_db_pr7",` +
		`"privileges":{"readWrite":["orders-pr7@acme-staging.iam","shop-api-pr7@acme-staging.iam","shop-orders-fulfil-orders-pr7@acme-staging.iam","shop-orders-ship-orders-pr7@acme-staging.iam"]}}`
	if string(got) != want {
		t.Errorf("the member's job: %s\nwant %s", got, want)
	}
}

// TestMigrationJobNeedsARelease: a binary built from a checkout names no
// runner release, so its first migration stops, saying how to name an
// image.
func TestMigrationJobNeedsARelease(t *testing.T) {
	f := newJobFixture(t, gcp.Extension{})
	f.ready(t)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	_, err := stack.Deploy(context.Background(), stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Images:  shopImages("acme-staging", 1),
		Planner: firstPlanner,
	})
	if err == nil || !strings.Contains(err.Error(), "built from a checkout") || !strings.Contains(err.Error(), gcp.MigrateImageEnv) {
		t.Fatalf("deploy = %v", err)
	}
	t.Setenv(gcp.MigrateImageEnv, migrateRepo+":latest")
	_, err = stack.Deploy(context.Background(), stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Images:  shopImages("acme-staging", 1),
		Planner: firstPlanner,
	})
	if err == nil || !strings.Contains(err.Error(), "is not pinned by digest") {
		t.Fatalf("deploy with a tagged runner image = %v", err)
	}
}

// TestBuilder: the builder finds an image built from the same context
// without building it, and a build that fails stops the deploy before it
// changes anything.
func TestBuilder(t *testing.T) {
	f := newJobFixture(t, gcp.Extension{MigrateVersion: "1.2.3"})
	f.ready(t)
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	src := shopSources(t)
	o := stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Sources: src,
		Planner: firstPlanner,
	}
	built, err := stack.Build(context.Background(), stack.BuildOptions{Options: o.Options, Sources: *src})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.cloud.builds) != 4 {
		t.Fatalf("stack build ran %d builds", len(f.cloud.builds))
	}
	f.cloud.failBuild[migrateRepo+":1.2.3"] = "step 0 exited 1"
	_, err = stack.Deploy(context.Background(), o)
	if len(f.cloud.builds) != 5 || err == nil || !strings.Contains(err.Error(), "step 0 exited 1") {
		t.Fatalf("deploy = %v after %d builds", err, len(f.cloud.builds))
	}
	data, rerr := f.cloud.ReadObject(context.Background(), stateBucket, "superschematic/manifests/shop/Staging.json")
	if rerr != nil {
		t.Fatal(rerr)
	}
	m, rerr := stack.UnmarshalManifest(data)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if m.Status != stack.StatusFailed || m.Step != "migrate expand" || len(m.Images) > 0 {
		t.Errorf("after the runner's build failed: %s at %s, images %v", m.Status, m.Step, m.Images)
	}
	f.cloud.failBuild = map[string]string{}
	m2, err := stack.Deploy(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.cloud.builds) != 6 || m2.Images["Orders"] != built.Images["Orders"] {
		t.Errorf("the next deploy ran %d builds in all and runs Orders at %s, want stack build's %s", len(f.cloud.builds), m2.Images["Orders"], built.Images["Orders"])
	}

	// A server's build that fails stops the deploy before anything.
	f2 := newJobFixture(t, gcp.Extension{MigrateVersion: "1.2.3"})
	f2.ready(t)
	o.Registry = f2.reg
	f2.cloud.failBuild[shopRepo+"orders:"] = "the compiler failed"
	if _, err := stack.Deploy(context.Background(), o); err == nil || !strings.Contains(err.Error(), "the compiler failed") {
		t.Fatalf("deploy = %v", err)
	}
	if got := steps(f2.prov.Calls()); !slices.Equal(got, []string{"cloud build " + shopRepo + "orders:context-<hex>"}) {
		t.Errorf("a failed build went on to:\n%s", strings.Join(got, "\n"))
	}
}
