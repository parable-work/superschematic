package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// rolePassword is the password of every role roles creates. A server that
// trusts its clients ignores it; one that asks for a password, as CI's
// does, needs it.
const rolePassword = "superschematic-role"

// roles creates login roles named after names, unique to the run, with
// rolePassword, and drops them when the test ends, after the test's
// databases are dropped, so call it before testdb.NewPostgres. It skips
// without a server.
func roles(t *testing.T, names ...string) []string {
	t.Helper()
	server := os.Getenv(testdb.EnvURL)
	if server == "" {
		t.Skipf("set %s to run jobs against Postgres", testdb.EnvURL)
	}
	var out []string
	for _, name := range names {
		out = append(out, fmt.Sprintf("%s-%d@acme-staging.iam", name, time.Now().UnixNano()))
	}
	testdb.Exec(t, server, func() []string {
		var statements []string
		for _, role := range out {
			statements = append(statements, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+rolePassword+"'")
		}
		return statements
	}()...)
	t.Cleanup(func() {
		for _, role := range out {
			testdb.Exec(t, server, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
		}
	})
	return out
}

// writeJob writes a job document to a new directory and returns its path.
func writeJob(t *testing.T, job *migrate.Job) string {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// can reports whether role holds privilege on the table or sequence.
func can(t *testing.T, dbURL, role, object, privilege string) bool {
	t.Helper()
	fn := "has_table_privilege"
	if strings.HasSuffix(object, "_seq") {
		fn = "has_sequence_privilege"
	}
	got := testdb.Strings(t, dbURL, "SELECT "+fn+"($1, $2, $3)::text", role, object, privilege)
	return len(got) == 1 && got[0] == "true"
}

// asRole is dbURL with role, a role roles created, as its user.
func asRole(t *testing.T, dbURL, role string) string {
	t.Helper()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, rolePassword)
	return u.String()
}

// TestJob runs job documents on Postgres: a plan's phase, then the read
// and write privileges of the roles it lists, which a later job takes
// back from a role it no longer lists.
func TestJob(t *testing.T) {
	r := roles(t, "shop-api", "orders")
	api, orders := r[0], r[1]
	dbURL := testdb.NewPostgres(t)
	create := fixturePath(migrate.Postgres, "01-create")
	createAbs, err := filepath.Abs(create)
	if err != nil {
		t.Fatal(err)
	}
	job := &migrate.Job{Version: migrate.JobVersion, Phase: migrate.All, Databases: []*migrate.JobDatabase{{
		Service: "shop", DatabaseURL: dbURL, Plan: createAbs,
		Privileges: &migrate.Privileges{ReadWrite: []string{api, orders}},
	}}}
	invoke(t, nil, "job", "--job", writeJob(t, job)).
		expect(t, exitOK, "plan ", "service shop: read and write privileges on 2 object(s) in 1 schema(s) for "+api+", "+orders)
	for _, role := range []string{api, orders} {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			if !can(t, dbURL, role, `public."order"`, privilege) {
				t.Errorf("%s cannot %s order", role, privilege)
			}
		}
		if can(t, dbURL, role, "public.superschematic_schema_state", "SELECT") || can(t, dbURL, role, `public."order"`, "TRUNCATE") {
			t.Errorf("%s holds more than read and write on the tables", role)
		}
	}
	// The role works the table as a server would.
	testdb.Exec(t, asRole(t, dbURL, api),
		`INSERT INTO customer (id, name) VALUES (1, 'Ada')`,
		`INSERT INTO "order" (id, customer_id, total) VALUES (1, 1, 5)`,
		`UPDATE "order" SET total = 6 WHERE id = 1`)

	// The next phase, beside the plan: orders no longer connects.
	evolve, err := filepath.Abs(fixturePath(migrate.Postgres, "02-evolve"))
	if err != nil {
		t.Fatal(err)
	}
	job.Phase, job.Databases[0].Plan = migrate.Expand, evolve
	job.Databases[0].Privileges.ReadWrite = []string{api}
	invoke(t, nil, "job", "--job", writeJob(t, job)).
		expect(t, exitOK, "expand is done", "took the privileges of "+orders+" back")
	if can(t, dbURL, orders, `public."order"`, "SELECT") || !can(t, dbURL, api, "public.customer", "UPDATE") {
		t.Error("the second job left the privileges wrong")
	}

	// Privileges alone, for no role: every grant goes.
	job.Databases[0].Plan = ""
	job.Databases[0].Privileges.ReadWrite = []string{}
	invoke(t, nil, "job", "--job", writeJob(t, job)).expect(t, exitOK, "for no role", "took the privileges of "+api+" back")
	if can(t, dbURL, api, `public."order"`, "SELECT") {
		t.Error("a job for no role left a grant")
	}
}

// TestJobRefusals: a malformed job document is refused before it runs.
func TestJobRefusals(t *testing.T) {
	invoke(t, nil, "job").expect(t, exitUsage, "job needs --job")
	path := filepath.Join(t.TempDir(), "job.json")
	for doc, want := range map[string]string{
		`{"version": 2, "phase": "all", "databases": []}`:                                                                                                                                   "version 2",
		`{"version": 1, "phase": "later", "databases": []}`:                                                                                                                                 `phase "later"`,
		`{"version": 1, "phase": "all", "databases": []}`:                                                                                                                                   "no databases",
		`{"version": 1, "phase": "all", "databases": [{"service": "shop", "databaseUrl": "x"}]}`:                                                                                            "neither a plan nor privileges",
		`{"version": 1, "phase": "all", "databases": [{"service": "shop", "database": "shop", "plan": "p"}]}`:                                                                               "names each database by its URL",
		`{"version": 1, "phase": "all", "cloudSql": {"instance": "acme", "user": "u@p.iam"}, "databases": []}`:                                                                              "is not a connection name",
		`{"version": 1, "phase": "all", "cloudSql": {"instance": "acme-staging:us-east1:shop-db", "user": "u@p.iam.gserviceaccount.com"}, "databases": []}`:                                 "want the IAM database user",
		`{"version": 1, "phase": "all", "cloudSql": {"instance": "acme-staging:us-east1:shop-db", "user": "u@p.iam"}, "databases": [{"service": "shop", "databaseUrl": "x", "plan": "p"}]}`: "not a URL",
		`{"version": 1, "phase": "all", "databases": [{"service": "shop", "databaseUrl": "x", "privileges": {"readWrite": ["a", "a"]}}]}`:                                                   "role a is listed twice",
		`{"version": 1, "phase": "all", "databases": [], "extra": true}`:                                                                                                                    "unknown field",
	} {
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		invoke(t, nil, "job", "--job", path).expect(t, exitFailed, want)
	}
}

// fakeStorage serves objects over Cloud Storage's JSON API, as an emulator
// does, and records each path it was asked for.
type fakeStorage struct {
	objects map[string][]byte
	asked   []string
}

func (s *fakeStorage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.asked = append(s.asked, r.URL.EscapedPath())
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/storage/v1/b/")
	bucket, object, _ := strings.Cut(rest, "/o/")
	name, err := url.PathUnescape(object)
	data, found := s.objects[bucket+"/"+name]
	if !ok || err != nil || r.URL.Query().Get("alt") != "media" || !found {
		http.Error(w, `{"error": {"code": 404, "message": "No such object"}}`, http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}

// localDialer dials a local Postgres server in place of a Cloud SQL
// instance, and records the instance it was made for.
type localDialer struct {
	addr     string
	instance string
}

func (d *localDialer) Dial(ctx context.Context, _, _ string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", d.addr)
}

func (d *localDialer) Close() error { return nil }

// TestJobOnCloudSQL runs a job as a deploy on gcp writes it: the job
// document and its plan in a bucket, read through Cloud Storage's API,
// and the database reached through the Cloud SQL connector, here a dialer
// of the local server, as an IAM database user that owns what it creates.
func TestJobOnCloudSQL(t *testing.T) {
	r := roles(t, "shop-migrator", "shop-api")
	migrator, api := r[0], r[1]
	// On Cloud SQL the connector logs the IAM database user in, so the
	// job's connection string names no password. The local server may ask
	// for one, which pgx then reads from PGPASSWORD.
	t.Setenv("PGPASSWORD", rolePassword)
	dbURL := testdb.NewPostgres(t)
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	database := strings.TrimPrefix(u.Path, "/")
	testdb.Exec(t, dbURL,
		"GRANT CREATE ON SCHEMA public TO "+pgx.Identifier{migrator}.Sanitize(),
		"GRANT CONNECT, TEMPORARY ON DATABASE "+pgx.Identifier{database}.Sanitize()+" TO "+pgx.Identifier{migrator}.Sanitize())

	plan, err := os.ReadFile(fixturePath(migrate.Postgres, "01-create"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := json.Marshal(&migrate.Job{
		Version: migrate.JobVersion, Phase: migrate.Expand,
		CloudSQL: &migrate.CloudSQLConnection{Instance: "acme-staging:us-east1:shop-db", User: migrator},
		Databases: []*migrate.JobDatabase{{
			Service: "shop", Database: database, Plan: "../plans/create.json",
			Privileges: &migrate.Privileges{ReadWrite: []string{api}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	storage := &fakeStorage{objects: map[string][]byte{
		"state/superschematic/migrations/shop/Staging/shop-db/expand.json": job,
		"state/superschematic/migrations/shop/Staging/plans/create.json":   plan,
	}}
	server := httptest.NewServer(storage)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	dialer := &localDialer{addr: u.Host}
	o := options{
		stdout: &stdout, stderr: &stderr,
		getenv: func(key string) string {
			if key == storageEmulatorEnv {
				return server.URL
			}
			return ""
		},
		cloudSQL: func(_ context.Context, instance string) (sqlDialer, error) {
			dialer.instance = instance
			return dialer, nil
		},
	}
	code := runWith(context.Background(), []string{"job", "--job", "gs://state/superschematic/migrations/shop/Staging/shop-db/expand.json"}, o)
	t.Logf("job: exit %d\n%s%s", code, stdout.String(), stderr.String())
	if code != exitOK {
		t.Fatalf("job exit %d: %s", code, stderr.String())
	}
	if dialer.instance != "acme-staging:us-east1:shop-db" {
		t.Errorf("dialed %q", dialer.instance)
	}
	want := []string{
		"/storage/v1/b/state/o/superschematic%2Fmigrations%2Fshop%2FStaging%2Fshop-db%2Fexpand.json",
		"/storage/v1/b/state/o/superschematic%2Fmigrations%2Fshop%2FStaging%2Fplans%2Fcreate.json",
	}
	if strings.Join(storage.asked, "\n") != strings.Join(want, "\n") {
		t.Errorf("read %q, want %q", storage.asked, want)
	}
	owners := testdb.Strings(t, dbURL, `SELECT tableowner FROM pg_tables WHERE tablename = 'order'`)
	if len(owners) != 1 || owners[0] != migrator {
		t.Errorf("order is owned by %v, want the migrator", owners)
	}
	if !can(t, dbURL, api, `public."order"`, "INSERT") {
		t.Error("the server's role cannot write order")
	}

	// A missing object is named.
	code = runWith(context.Background(), []string{"job", "--job", "gs://state/nothing.json"}, o)
	if code != exitFailed || !strings.Contains(stderr.String(), "gs://state/nothing.json: file does not exist") {
		t.Errorf("a missing job: exit %d, %s", code, stderr.String())
	}
}

// TestRelativeTo resolves a plan beside its job document.
func TestRelativeTo(t *testing.T) {
	for _, tc := range [][3]string{
		{"gs://b/jobs/x/job.json", "plan.json", "gs://b/jobs/x/plan.json"},
		{"gs://b/jobs/x/job.json", "../plans/p.json", "gs://b/jobs/plans/p.json"},
		{"gs://b/jobs/job.json", "gs://c/p.json", "gs://c/p.json"},
		{"/tmp/jobs/job.json", "p.json", "/tmp/jobs/p.json"},
		{"/tmp/jobs/job.json", "/plans/p.json", "/plans/p.json"},
	} {
		if got := relativeTo(tc[0], tc[1]); got != tc[2] {
			t.Errorf("relativeTo(%s, %s) = %s, want %s", tc[0], tc[1], got, tc[2])
		}
	}
}
