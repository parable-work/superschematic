package gcp_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// rustOrdersShop is the acme-shop services with shop-orders written in
// Rust (D65): its server Orders, its job ShipOrders and its worker
// FulfilOrders are Rust, and shop-api, in Go, keeps its bucket.
func rustOrdersShop() []stack.Service {
	return stacktest.InRust(stacktest.AcmeShop(), stacktest.ShopOrders.Name)
}

// TestRustJobAndWorker: with shop-orders in Rust, its job is a Cloud Run
// job and its worker a Cloud Run worker pool, as a Go API's are (D52, D53,
// D65), and each lowers as a Go one does: each runs the image its
// entrypoint's Dockerfile builds. Each one's sql edge gives it what a Go
// one's gives, the grants, the IAM database user and the Cloud SQL
// connection, and its Cloud Run resource mounts the instance's socket at
// /cloudsql, where the Rust runtime dials it as that user (D57).
func TestRustJobAndWorker(t *testing.T) {
	reg := assemble(t)
	goEnv := resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	env := resolve(t, reg, shop(), rustOrdersShop(), "Staging")
	for name, want := range map[string]struct {
		kind      ir.DeployableKind
		platform  string
		connector string
		node      string
		container func(props map[string]any) map[string]any
	}{
		stacktest.ShipOrdersJob: {ir.DeployableJob, gcp.CloudRunJob, gcp.JobSQLConnector, stacktest.ShipOrdersJob + ".job", func(props map[string]any) map[string]any {
			return props["template"].(map[string]any)["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		}},
		stacktest.FulfilOrdersWorker: {ir.DeployableWorker, gcp.CloudRunWorker, gcp.WorkerSQLConnector, stacktest.FulfilOrdersWorker + ".worker-pool", func(props map[string]any) map[string]any {
			return props["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		}},
	} {
		d := env.Deployable(name)
		if d == nil || d.Kind != want.kind || d.Platform != want.platform || d.Language != registry.APILanguageRust {
			t.Fatalf("%s is %+v, want a Rust %s on %s", name, d, want.kind, want.platform)
		}
		edge := "sql:" + name + "->shop-db"
		i := slices.IndexFunc(env.Edges, func(e *ir.Edge) bool { return e.ID == edge })
		if i < 0 || env.Edges[i].Connector != want.connector {
			t.Fatalf("%s is not connected by %s", edge, want.connector)
		}
		ids, goIDs := ownedBy(env, edge), ownedBy(goEnv, edge)
		if want := name + ".cloudsql-client.shop-db, " + name + ".cloudsql-login.shop-db, " + name + ".database-user.shop-db"; strings.Join(ids, ", ") != want || !slices.Equal(ids, goIDs) {
			t.Errorf("%s produces %v, want %s, as a Go %s's produces %v", edge, ids, want, d.Kind, goIDs)
		}
		for _, id := range append(ids, want.node) {
			wantJSON(t, id, node(t, env, id).Properties, mustJSON(t, node(t, goEnv, id).Properties))
		}
		wantJSON(t, name+"'s connection", binding(t, env, name, "SHOP_DB_DATABASE").Value,
			`{"cloudSql":{"database":{"$output":{"resource":"shop-db.database.shop-db","name":"name"}},"instance":{"$output":{"resource":"shop-db.instance","name":"connectionName"}},"user":"`+name+`@acme-staging.iam"}}`)
		wantJSON(t, name+"'s Cloud SQL mount", want.container(node(t, env, want.node).Properties)["volumeMounts"], `[{"mountPath":"/cloudsql","name":"cloudsql"}]`)
	}
}

// TestBucketConnectorRefusesARustJobAndWorker: with shop-orders in Rust
// listing shop-media, its job's and its worker's bucket edges, which are
// its own (D52, D53), are refused as its server's is: the Rust runtime has
// no Bucket, and D57 leaves buckets from a Rust server out. Nothing else
// fails.
func TestBucketConnectorRefusesARustJobAndWorker(t *testing.T) {
	services := stacktest.InRust(withBuckets([]string{stacktest.ShopOrders.Name}, stacktest.ShopMedia), stacktest.ShopOrders.Name)
	_, err := stack.Resolve(assemble(t), stack.Input{Stack: shop(), Services: services, Environment: "Staging"})
	var errs *stack.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("err = %v, want resolution errors", err)
	}
	var got []string
	for _, e := range errs.List {
		if e.Code != stack.CodeLowering {
			t.Errorf("a %s failure: %s", e.Code, e.Message)
		}
		got = append(got, e.Message)
	}
	for _, d := range []string{"server Orders", "job " + stacktest.ShipOrdersJob, "worker " + stacktest.FulfilOrdersWorker} {
		kind, _, _ := strings.Cut(d, " ")
		want := d + " is a RUST " + kind + ", which has no Bucket in its runtime to reach bucket shop-media with: buckets from a Rust server are not built (D57)"
		if !slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, want) }) {
			t.Errorf("no failure says %q in:\n%s", want, strings.Join(got, "\n"))
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d failures, want the three bucket edges':\n%s", len(got), strings.Join(got, "\n"))
	}
}
