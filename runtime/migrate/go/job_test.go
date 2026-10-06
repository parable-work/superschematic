package migrate_test

import (
	"os"
	"path/filepath"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// TestReadsTheGCPJob reads the job document the gcp target writes for a
// deploy's expand phase, checked in as its golden: the contract between
// the target, which does not import the runner, and the runner.
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
		d.Database != "shop_db" || d.Privileges == nil || len(d.Privileges.ReadWrite) != 2 {
		t.Errorf("job %+v, database %+v", job, d)
	}
}
