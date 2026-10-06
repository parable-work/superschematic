// Package cloudsql opens the runner's Postgres driver on a Cloud SQL
// instance through the Cloud SQL Go connector, with IAM database
// authentication: the connector dials the instance by its connection name
// over TLS with an ephemeral certificate, and logs in as the IAM database
// user of the service account the job runs as, so there is no password.
// The instance refuses any connection that does not come through a
// connector (D46).
package cloudsql

import (
	"context"
	"fmt"
	"net"
	"net/url"

	"cloud.google.com/go/cloudsqlconn"

	"github.com/parable-work/superschematic/runtime/migrate/go/postgres"
)

// Dialer reaches one Cloud SQL instance.
type Dialer struct {
	instance string
	dialer   *cloudsqlconn.Dialer
}

// NewDialer returns a dialer of the instance whose connection name is
// instance, `project:region:name`, authenticating with application default
// credentials: on Cloud Run, the account the job runs as. Close it when
// the run ends.
func NewDialer(ctx context.Context, instance string) (*Dialer, error) {
	d, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN(), cloudsqlconn.WithUserAgent("superschematic-migrate"))
	if err != nil {
		return nil, fmt.Errorf("cloudsql: the Cloud SQL connector: %w", err)
	}
	return &Dialer{instance: instance, dialer: d}, nil
}

// Dial dials the instance, whatever network and address it is handed.
func (d *Dialer) Dial(ctx context.Context, _, _ string) (net.Conn, error) {
	conn, err := d.dialer.Dial(ctx, d.instance)
	if err != nil {
		return nil, fmt.Errorf("cloudsql: dial %s: %w", d.instance, err)
	}
	return conn, nil
}

// Close closes the connector.
func (d *Dialer) Close() error { return d.dialer.Close() }

// URL is the connection string of database on the instance as user: the
// connector carries TLS and IAM authentication, so the string names no
// host to reach, no password and no TLS of its own.
func URL(user, database string) string {
	u := url.URL{Scheme: "postgres", User: url.User(user), Host: "cloudsql", Path: "/" + database, RawQuery: "sslmode=disable"}
	return u.String()
}

// Open opens the runner's Postgres driver on database as user through the
// dialer.
func (d *Dialer) Open(ctx context.Context, user, database string, opts postgres.Options) (*postgres.Driver, error) {
	opts.Dial = d.Dial
	return postgres.Open(ctx, URL(user, database), opts)
}
