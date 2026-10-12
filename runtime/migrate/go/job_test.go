package migrate_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// TestReadsTheGCPJob reads the job document the gcp target writes for a
// deploy's expand phase, checked in as its golden: the contract between
// the target, which does not import the runner, and the runner. Four
// users connect: the two servers', shop-orders' job's (D52) and its
// worker's (D53).
func TestReadsTheGCPJob(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "extensions", "gcp", "testdata", "golden", "migrations", "Staging-expand.json"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := migrate.ReadJob(doc)
	if err != nil {
		t.Fatal(err)
	}
	d := job.Databases[0]
	if job.Phase != migrate.Expand || job.CloudSQL == nil || job.CloudSQL.Instance != "acme-staging:us-east1:shop-db" ||
		d.Database != "shop_db" || d.Privileges == nil {
		t.Fatalf("job %+v, database %+v", job, d)
	}
	want := []string{
		"orders@acme-staging.iam",                    // the orders server
		"shop-api@acme-staging.iam",                  // the shop-api server
		"shop-orders-fulfil-orders@acme-staging.iam", // shop-orders' worker (D53)
		"shop-orders-ship-orders@acme-staging.iam",   // shop-orders' job (D52)
	}
	if got := slices.Sorted(slices.Values(d.Privileges.ReadWrite)); !slices.Equal(got, want) {
		t.Errorf("read-write users %q, want %q", got, want)
	}
}
