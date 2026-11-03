package gcp_test

import (
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// longNamesShop is the acme-shop services with a job and a worker more on
// shop-orders, named as a finances API names its own: SyncAllPlaidItems, whose
// deployable shop-orders-sync-all-plaid-items is 32 characters, and
// ReconcileEveryLinkedAccount, 39, both past a service account id's 30.
func longNamesShop() []stack.Service {
	services := stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == stacktest.ShopOrders.Name {
			services[i].Jobs = append(slices.Clone(services[i].Jobs), ir.Job{Name: "SyncAllPlaidItems", Schedule: "0 */6 * * *", Timeout: "10m"})
			worker := stacktest.FulfilOrders
			worker.Name = "ReconcileEveryLinkedAccount"
			services[i].Workers = append(slices.Clone(services[i].Workers), worker)
		}
	}
	return services
}

// TestLongNamesGolden resolves LongShop, the shop over longNamesShop, in
// Staging and in Preview's member (D97): the job's Cloud Run job, its
// Cloud Scheduler job and its service account share one name of 30
// characters, a prefix of the deployable's and a hash of it, as the
// worker's pool and account do, and the job and the pool carry the
// deployable's whole name in the label superschematic-deployable. In the
// member the name leaves six characters for the pull request's number.
// Every name that fits, shop-api's and Orders' among them, is as
// TestGolden has it.
func TestLongNamesGolden(t *testing.T) {
	reg := assemble(t)
	s := shop()
	s.Name = "LongShop"
	for _, env := range []string{"Staging", "Preview"} {
		t.Run(env, func(t *testing.T) {
			checkGolden(t, reg, s, longNamesShop(), env)
		})
	}

	env := resolve(t, reg, s, longNamesShop(), "Staging")
	job := node(t, env, "shop-orders-sync-all-plaid-items.job").Properties
	name, _ := job["name"].(string)
	if !strings.HasPrefix(name, "shop-orders-sync-all-") || len(name) > 30 {
		t.Errorf("the Cloud Run job is %q, want a prefix and a hash in 30 characters", name)
	}
	for _, id := range []string{"shop-orders-sync-all-plaid-items.schedule"} {
		if got := node(t, env, id).Properties["name"]; got != name {
			t.Errorf("%s is named %v, want %s, the job's name", id, got, name)
		}
	}
	if got := node(t, env, "shop-orders-sync-all-plaid-items.account").Properties["accountId"]; got != name {
		t.Errorf("the job's account is %v, want %s", got, name)
	}
	wantJSON(t, "the job's labels", job["labels"], `{"superschematic-deployable":"shop-orders-sync-all-plaid-items"}`)
	if labels := node(t, env, "shop-api.service").Properties["labels"]; labels != nil {
		t.Errorf("shop-api, whose name fits, has labels %v", labels)
	}
}
