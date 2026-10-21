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
	driver, closeDriver, err := queueDriver(ctx, q, conn, *database, *urlFlag, o)
	if err != nil {
		return err
	}
	defer closeDriver()
	result, err := migrate.RunQueue(ctx, driver, q)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if *resultRef == "" {
		_, err = o.stdout.Write(out)
		return err
	}
	if err := o.resultWriter().WriteResult(ctx, *resultRef, out); err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.stdout, "queue %s of %s: wrote the result of %s to %s\n", q.Queue, q.Service, q.Operation, *resultRef)
	return err
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

// resultWriter writes a queue operation's result where --result names it.
// storageWriter is the real one; tests fake it.
type resultWriter interface {
	WriteResult(ctx context.Context, ref string, data []byte) error
}

func (o options) resultWriter() resultWriter {
	if o.results != nil {
		return o.results
	}
	return storageWriter{o: o}
}

// storageWriter writes a result to a Cloud Storage object a gs:// URL
// names, with application default credentials (on Cloud Run, the account
// the job runs as), or to a file. Either must not exist yet: the object is
// written with ifGenerationMatch=0, which only object create permission
// allows, and the file is created with mode 0600.
type storageWriter struct{ o options }

// readWriteScope is the OAuth scope a gs:// write needs.
const readWriteScope = "https://www.googleapis.com/auth/devstorage.read_write"

func (w storageWriter) WriteResult(ctx context.Context, ref string, data []byte) error {
	rest, ok := strings.CutPrefix(ref, "gs://")
	if !ok {
		f, err := os.OpenFile(ref, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("write the result to %s: %w", ref, err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write the result to %s: %w", ref, err)
		}
		return f.Close()
	}
	bucket, object, _ := strings.Cut(rest, "/")
	if bucket == "" || object == "" {
		return fmt.Errorf("%s: want gs://<bucket>/<object>", ref)
	}
	base, token, err := w.o.storage(ctx, readWriteScope)
	if err != nil {
		return fmt.Errorf("write the result to %s: %w", ref, err)
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
		return fmt.Errorf("write the result to %s: %w", ref, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusPreconditionFailed:
		return fmt.Errorf("write the result to %s: %w", ref, fs.ErrExist)
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return errors.New("write the result to " + ref + ": " + resp.Status + ": " + msg)
}
