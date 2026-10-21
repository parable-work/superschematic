package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/cloudsql"
	"github.com/parable-work/superschematic/runtime/migrate/go/postgres"
)

// queue runs a queue document (migrate.QueueDocument) on a Postgres
// database: the one --database-url names, or one on a Cloud SQL instance,
// reached through the Cloud SQL Go connector as a job's databases are. It
// prints what it did as JSON (migrate.QueueResult), or, with --result,
// writes that to the object or file --result names and prints only where
// it went: a listing holds the dead messages' bodies, which may hold
// personal data, and a job's stdout and stderr go to Cloud Logging (D68,
// amended).
func queue(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("queue", o)
	ref := fs.String("queue", "", "the queue `document`: a path, or a gs:// URL")
	instance := fs.String("cloud-sql", "", "the Cloud SQL `instance`'s connection name, project:region:instance, in place of --database-url")
	user := fs.String("cloud-sql-user", "", "the IAM database `user` to connect to the Cloud SQL instance as")
	database := fs.String("database", "", "the `name` of the database on the Cloud SQL instance")
	resultRef := fs.String("result", "", "write the result to this `object`, a gs:// URL or a path, which must not exist, in place of stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ref == "" {
		return usageErrorf("queue needs --queue")
	}
	var conn *migrate.CloudSQLConnection
	switch {
	case *instance != "" && *urlFlag != "":
		return usageErrorf("queue takes --database-url or --cloud-sql, not both")
	case *instance != "":
		if *user == "" || *database == "" {
			return usageErrorf("--cloud-sql needs --cloud-sql-user and --database")
		}
		conn = &migrate.CloudSQLConnection{Instance: *instance, User: *user}
		if err := conn.Check(); err != nil {
			return usageErrorf("%v", err)
		}
	case *user != "" || *database != "":
		return usageErrorf("--cloud-sql-user and --database name a database on the instance --cloud-sql names")
	}
	doc, err := o.read(ctx, *ref)
	if err != nil {
		return err
	}
	q, err := migrate.ReadQueueDocument(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", *ref, err)
	}
	// The result's place is reserved before the operation runs, so a
	// runner that may not write it there changes nothing.
	var reserved resultReservation
	if *resultRef != "" {
		if reserved, err = o.resultWriter().Reserve(ctx, *resultRef); err != nil {
			return fmt.Errorf("queue %s of %s: %w; the queue was not changed", q.Queue, q.Service, err)
		}
	}
	out, err := runQueueDocument(ctx, q, conn, *database, *urlFlag, o)
	if err != nil {
		if reserved != nil {
			reserved.Abandon()
		}
		return err
	}
	if reserved == nil {
		_, err = o.stdout.Write(out)
		return err
	}
	if err := reserved.Write(ctx, out); err != nil {
		if q.Operation == migrate.QueueDead {
			return err
		}
		return fmt.Errorf("queue %s of %s: the %s was applied, and its result was not written: %w; superschematic stack queue dead lists the dead messages as they are now",
			q.Queue, q.Service, q.Operation, err)
	}
	_, err = fmt.Fprintf(o.stdout, "queue %s of %s: wrote the result of %s to %s\n", q.Queue, q.Service, q.Operation, *resultRef)
	return err
}

// runQueueDocument runs q on its database and returns its result as
// indented JSON.
func runQueueDocument(ctx context.Context, q *migrate.QueueDocument, conn *migrate.CloudSQLConnection, database, urlFlag string, o options) ([]byte, error) {
	driver, closeDriver, err := queueDriver(ctx, q, conn, database, urlFlag, o)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	result, err := migrate.RunQueue(ctx, driver, q)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// queueDriver opens the Postgres database a queue document runs on: on
// the Cloud SQL instance conn names, or at the URL --database-url or
// DATABASE_URL names.
func queueDriver(ctx context.Context, q *migrate.QueueDocument, conn *migrate.CloudSQLConnection, database, urlFlag string, o options) (migrate.Driver, func(), error) {
	if conn != nil {
		dial := o.cloudSQL
		if dial == nil {
			dial = newCloudSQLDialer
		}
		dialer, err := dial(ctx, conn.Instance)
		if err != nil {
			return nil, nil, err
		}
		d, err := postgres.Open(ctx, cloudsql.URL(conn.User, database), postgres.Options{Dial: dialer.Dial})
		if err != nil {
			_ = dialer.Close()
			return nil, nil, err
		}
		return d, func() {
			_ = d.Close(context.WithoutCancel(ctx))
			_ = dialer.Close()
		}, nil
	}
	url, err := databaseURL(urlFlag, o)
	if err != nil {
		return nil, nil, err
	}
	// A queue's messages are claimed on Postgres alone, so no other
	// database is opened for them.
	if dialect := migrate.URLDialect(url); dialect != migrate.Postgres {
		return nil, nil, fmt.Errorf("queue %s of %s: a queue's messages are claimed on Postgres alone, and the database URL selects %s", q.Queue, q.Service, dialect)
	}
	return openDriver(ctx, url, o)
}

// resultWriter reserves the place --result names before the operation
// runs, and the reservation writes the result there after it: a write the
// runner may not make then fails before the queue changes. storageWriter
// is the real one; tests fake it.
type resultWriter interface {
	Reserve(ctx context.Context, ref string) (resultReservation, error)
}

// resultReservation is a reserved place for a result.
type resultReservation interface {
	// Write writes the result to the place.
	Write(ctx context.Context, data []byte) error
	// Abandon gives up the place when the operation failed. An object's
	// marker stays, for whoever reads the result to delete.
	Abandon()
}

func (o options) resultWriter() resultWriter {
	if o.results != nil {
		return o.results
	}
	return storageWriter{o: o}
}

// reservedSuffix ends the name of the empty object that reserves a
// result's object: <object>.reserved. The runner may create objects there
// and not delete them, so whoever reads the result deletes the marker too.
const reservedSuffix = ".reserved"

// storageWriter writes a result to a Cloud Storage object a gs:// URL
// names, with application default credentials (on Cloud Run, the account
// the job runs as), or to a file. Either must not exist yet: an object is
// written with ifGenerationMatch=0, which only object create permission
// allows, and a file is created with mode 0600. An object is reserved by
// an empty marker beside it, written the same way, which proves the
// runner may create objects there before the operation runs; a file by
// its creation.
type storageWriter struct{ o options }

// readWriteScope is the OAuth scope a gs:// write needs.
const readWriteScope = "https://www.googleapis.com/auth/devstorage.read_write"

func (w storageWriter) Reserve(ctx context.Context, ref string) (resultReservation, error) {
	if !strings.HasPrefix(ref, "gs://") {
		f, err := os.OpenFile(ref, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("reserve %s for the result: %w", ref, err)
		}
		return fileReservation{f: f}, nil
	}
	if err := w.upload(ctx, ref+reservedSuffix, nil); err != nil {
		return nil, fmt.Errorf("reserve %s for the result: %w", ref, err)
	}
	return objectReservation{w: w, ref: ref}, nil
}

type fileReservation struct{ f *os.File }

func (r fileReservation) Write(_ context.Context, data []byte) error {
	if _, err := r.f.Write(data); err != nil {
		_ = r.f.Close()
		return fmt.Errorf("write the result to %s: %w", r.f.Name(), err)
	}
	return r.f.Close()
}

func (r fileReservation) Abandon() {
	_ = r.f.Close()
	_ = os.Remove(r.f.Name())
}

type objectReservation struct {
	w   storageWriter
	ref string
}

func (r objectReservation) Write(ctx context.Context, data []byte) error {
	if err := r.w.upload(ctx, r.ref, data); err != nil {
		return fmt.Errorf("write the result to %s: %w", r.ref, err)
	}
	return nil
}

func (objectReservation) Abandon() {}

// upload creates the object ref names with data, only where none is.
func (w storageWriter) upload(ctx context.Context, ref string, data []byte) error {
	bucket, object, _ := strings.Cut(strings.TrimPrefix(ref, "gs://"), "/")
	if bucket == "" || object == "" {
		return fmt.Errorf("%s: want gs://<bucket>/<object>", ref)
	}
	base, token, err := w.o.storage(ctx, readWriteScope)
	if err != nil {
		return err
	}
	u := base + "/upload/storage/v1/b/" + url.PathEscape(bucket) + "/o?uploadType=media&ifGenerationMatch=0&name=" + url.QueryEscape(object)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusPreconditionFailed:
		return fmt.Errorf("%s: %w", ref, fs.ErrExist)
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return errors.New(ref + ": " + resp.Status + ": " + msg)
}
