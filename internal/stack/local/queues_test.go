package local_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
)

// TestQueue runs a queue document with the migration runner on the
// database of the DB service that declares the queue, at the URL the
// environment resolved for it: it hands the runner the document and the
// URL, returns what the runner printed and removes the document. A
// refusal comes back as the runner's line, a database that does not
// answer says to run the environment with stack dev, and a service the
// environment hosts no database of, or a runner that is not there, is
// refused before anything runs.
func TestQueue(t *testing.T) {
	t.Setenv(local.MigrateEnv, "")
	f := newFixture(t, "Dev")
	url := local.DatabaseURL(f.pgPort, "shop_db")
	if got, err := local.DatabaseURLOf(f.env, "shop-db"); err != nil || got != url {
		t.Fatalf("DatabaseURLOf: %q, %v; want %q", got, err, url)
	}
	doc := []byte(`{"version": 1}` + "\n")
	ctx := context.Background()

	f.runner.rules = []rule{{prefix: "/bin/superschematic-migrate queue --queue ", times: 1, out: `{"version": 1, "ids": []}`}}
	out, err := f.prov.Queue(ctx, f.req, "shop-db", doc)
	if err != nil || string(out) != `{"version": 1, "ids": []}` {
		t.Fatalf("Queue: %q, %v", out, err)
	}
	cmd := f.runner.commands[len(f.runner.commands)-1]
	if len(cmd.Args) != 5 || cmd.Args[0] != "queue" || cmd.Args[1] != "--queue" || cmd.Args[3] != "--database-url" || cmd.Args[4] != url {
		t.Fatalf("the runner ran %q", cmd.String())
	}
	if _, err := os.Stat(cmd.Args[2]); !os.IsNotExist(err) {
		t.Errorf("the document %s outlived the run: %v", cmd.Args[2], err)
	}

	f.runner.rules = []rule{{prefix: "/bin/superschematic-migrate queue", times: 1,
		stderr: "superschematic-migrate: queue OrderPlaced of shop-db: 1 of the 1 message(s) named are not dead, so none was dropped: x is ready"}}
	if _, err := f.prov.Queue(ctx, f.req, "shop-db", doc); err == nil || err.Error() != "local: queue OrderPlaced of shop-db: 1 of the 1 message(s) named are not dead, so none was dropped: x is ready" {
		t.Fatalf("got %v", err)
	}

	f.runner.rules = []rule{{prefix: "/bin/superschematic-migrate queue", times: 1,
		stderr: fmt.Sprintf("superschematic-migrate: postgres: connect: failed to connect to `user=postgres database=shop_db`: 127.0.0.1:%d: dial error: connection refused", f.pgPort)}}
	if _, err := f.prov.Queue(ctx, f.req, "shop-db", doc); err == nil ||
		!strings.Contains(err.Error(), "local: the database of shop-db at "+url+" does not answer (postgres: connect: failed to connect") ||
		!strings.HasSuffix(err.Error(), "run the environment with superschematic stack dev --environment Dev, then run this from another terminal") {
		t.Fatalf("got %v", err)
	}

	n := len(f.runner.commands)
	if _, err := f.prov.Queue(ctx, f.req, "shop-api", doc); err == nil || err.Error() != "local: environment Dev hosts no database of shop-api" {
		t.Fatalf("got %v", err)
	}
	f.runner.missing = []string{local.MigrateBinary}
	if _, err := f.prov.Queue(ctx, f.req, "shop-db", doc); err == nil || !strings.Contains(err.Error(), "superschematic-migrate is not on PATH") {
		t.Fatalf("got %v", err)
	}
	if len(f.runner.commands) != n {
		t.Errorf("a refused operation ran %q", f.runner.lines("")[n:])
	}
}
