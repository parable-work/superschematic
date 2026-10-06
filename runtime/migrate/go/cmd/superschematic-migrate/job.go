package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cloud.google.com/go/auth/credentials"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/cloudsql"
	"github.com/parable-work/superschematic/runtime/migrate/go/postgres"
)

// sqlDialer reaches a Cloud SQL instance; cloudsql.Dialer is the real one,
// and tests dial a local server.
type sqlDialer interface {
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

func newCloudSQLDialer(ctx context.Context, instance string) (sqlDialer, error) {
	return cloudsql.NewDialer(ctx, instance)
}

// job runs a job document (migrate.Job): for each database in turn, the
// phase of its plan, then its privileges.
func job(ctx context.Context, args []string, o options) error {
	fs := flag.NewFlagSet(programName+" job", flag.ContinueOnError)
	fs.SetOutput(o.stderr)
	ref := fs.String("job", "", "the job `document`: a path, or a gs:// URL")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ref == "" {
		return usageErrorf("job needs --job")
	}
	doc, err := o.read(ctx, *ref)
	if err != nil {
		return err
	}
	j, err := migrate.ReadJob(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", *ref, err)
	}
	var dialer sqlDialer
	if j.CloudSQL != nil {
		dial := o.cloudSQL
		if dial == nil {
			dial = newCloudSQLDialer
		}
		if dialer, err = dial(ctx, j.CloudSQL.Instance); err != nil {
			return err
		}
		defer func() { _ = dialer.Close() }()
	}
	for _, db := range j.Databases {
		if err := runJobDatabase(ctx, j, db, dialer, *ref, o); err != nil {
			return fmt.Errorf("service %s: %w", db.Service, err)
		}
	}
	return nil
}

// runJobDatabase runs one database of a job.
func runJobDatabase(ctx context.Context, j *migrate.Job, db *migrate.JobDatabase, dialer sqlDialer, ref string, o options) error {
	var plan *migrate.Plan
	if db.Plan != "" {
		planRef := relativeTo(ref, db.Plan)
		doc, err := o.read(ctx, planRef)
		if err != nil {
			return err
		}
		if plan, err = migrate.ReadPlan(doc); err != nil {
			return fmt.Errorf("%s: %w", planRef, err)
		}
		if plan.Service != db.Service {
			return fmt.Errorf("%s is the plan of %s", planRef, plan.Service)
		}
	}
	var driver migrate.Driver
	var closeDriver func()
	if dialer != nil {
		d, err := postgres.Open(ctx, cloudsql.URL(j.CloudSQL.User, db.Database), postgres.Options{Dial: dialer.Dial})
		if err != nil {
			return err
		}
		driver, closeDriver = d, func() { _ = d.Close(context.WithoutCancel(ctx)) }
	} else {
		var err error
		if driver, closeDriver, err = openDriver(ctx, db.DatabaseURL, o); err != nil {
			return err
		}
	}
	defer closeDriver()
	if plan != nil {
		if plan.Dialect != driver.Dialect() {
			return fmt.Errorf("the plan is for %s and the database is %s", plan.Dialect, driver.Dialect())
		}
		if _, err := o.runner(driver).Apply(ctx, plan, j.Phase); err != nil {
			return err
		}
	}
	if db.Privileges == nil {
		return nil
	}
	granter, ok := driver.(migrate.Granter)
	if !ok {
		return fmt.Errorf("the %s driver gives no privileges; leave privileges out of the job", driver.Dialect())
	}
	grants, err := granter.GrantReadWrite(ctx, db.Privileges.ReadWrite, migrate.StateTables())
	if err != nil {
		return err
	}
	roles := "no role"
	if len(db.Privileges.ReadWrite) > 0 {
		roles = strings.Join(db.Privileges.ReadWrite, ", ")
	}
	_, _ = fmt.Fprintf(o.stdout, "service %s: read and write privileges on %d object(s) in %d schema(s) for %s\n", db.Service, grants.Objects, grants.Schemas, roles)
	if len(grants.Revoked) > 0 {
		_, _ = fmt.Fprintf(o.stdout, "service %s: took the privileges of %s back\n", db.Service, strings.Join(grants.Revoked, ", "))
	}
	return nil
}

// relativeTo resolves ref against the document at base: a gs:// URL or an
// absolute path stands as it is, and anything else is beside base.
func relativeTo(base, ref string) string {
	if strings.HasPrefix(ref, "gs://") || filepath.IsAbs(ref) {
		return ref
	}
	if rest, ok := strings.CutPrefix(base, "gs://"); ok {
		bucket, object, _ := strings.Cut(rest, "/")
		return "gs://" + bucket + "/" + path.Join(path.Dir(object), ref)
	}
	return filepath.Join(filepath.Dir(base), ref)
}

// storageEmulatorEnv names a Cloud Storage emulator's host, which a gs://
// URL is read from without credentials, as Google's client libraries do.
const storageEmulatorEnv = "STORAGE_EMULATOR_HOST"

// readOnlyScope is the OAuth scope a gs:// read needs.
const readOnlyScope = "https://www.googleapis.com/auth/devstorage.read_only"

// read returns a document: a file, or a Cloud Storage object a gs:// URL
// names, read with application default credentials (on Cloud Run, the
// account the job runs as).
func (o options) read(ctx context.Context, ref string) ([]byte, error) {
	rest, ok := strings.CutPrefix(ref, "gs://")
	if !ok {
		return os.ReadFile(ref)
	}
	bucket, object, _ := strings.Cut(rest, "/")
	if bucket == "" || object == "" {
		return nil, fmt.Errorf("%s: want gs://<bucket>/<object>", ref)
	}
	base := "https://storage.googleapis.com"
	token := ""
	if host := o.getenv(storageEmulatorEnv); host != "" {
		base = host
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
	} else {
		creds, err := credentials.DetectDefault(&credentials.DetectOptions{Scopes: []string{readOnlyScope}})
		if err != nil {
			return nil, fmt.Errorf("read %s: application default credentials: %w", ref, err)
		}
		t, err := creds.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("read %s: a token: %w", ref, err)
		}
		token = t.Value
	}
	u := strings.TrimSuffix(base, "/") + "/storage/v1/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(object) + "?alt=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ref, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ref, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("read %s: %w: %s", ref, os.ErrNotExist, msg)
		}
		return nil, errors.New("read " + ref + ": " + resp.Status + ": " + msg)
	}
	return body, nil
}
