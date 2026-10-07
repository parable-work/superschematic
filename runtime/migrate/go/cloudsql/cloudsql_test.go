package cloudsql

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestURL: the connection string names the IAM database user, escaped,
// and the database, with no password and no TLS of its own, since the
// connector carries both.
func TestURL(t *testing.T) {
	got := URL("shop-migrator@acme-staging.iam", "shop_db_pr123")
	if want := "postgres://shop-migrator%40acme-staging.iam@cloudsql/shop_db_pr123?sslmode=disable"; got != want {
		t.Fatalf("URL = %s, want %s", got, want)
	}
	config, err := pgx.ParseConfig(got)
	if err != nil {
		t.Fatal(err)
	}
	if config.User != "shop-migrator@acme-staging.iam" || config.Database != "shop_db_pr123" || config.Password != "" || config.TLSConfig != nil {
		t.Errorf("parsed user %q, database %q, password %q, TLS %v", config.User, config.Database, config.Password, config.TLSConfig != nil)
	}
}
