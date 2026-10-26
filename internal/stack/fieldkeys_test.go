package stack_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// TestFieldKeysReachEveryDeployableOnTheDatabase: the keys of a database's
// Encrypted<T> fields are an @envVars secret its config names (D85), so
// every server, job and worker that serves an API on the database reads
// that one secret, through the binding any Secret<T> field gets, and a
// deployable whose API's config lacks it is refused, naming the fix. Here
// shop-db names PaymentsSecrets.STRIPE_KEY, which both APIs' configs hold.
func TestFieldKeysReachEveryDeployableOnTheDatabase(t *testing.T) {
	reg := assemble(t)
	for _, tc := range []struct {
		name  string
		shop  func() (stack.Input, string)
		reads string
	}{
		{"a job", func() (stack.Input, string) {
			s, services := jobShop()
			return stack.Input{Stack: s, Services: services}, stacktest.ShipOrdersJob
		}, "Orders,shop-api," + stacktest.ShipOrdersJob},
		{"a worker", func() (stack.Input, string) {
			s, services := workerShop()
			return stack.Input{Stack: s, Services: services}, stacktest.FulfilOrdersWorker
		}, "Orders,shop-api," + stacktest.FulfilOrdersWorker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, deployable := tc.shop()
			service(in.Services, "shop-db").FieldKeys = "PaymentsSecrets.STRIPE_KEY"
			env := mustResolve(t, reg, in.Stack, in.Services, "Staging")
			if got := bindings(env.Deployable(deployable)); !strings.Contains(got, "STRIPE_KEY=secret:PaymentsSecrets.STRIPE_KEY") {
				t.Errorf("%s's bindings = %s", deployable, got)
			}
			if len(env.Secrets) != 1 || env.Secrets[0].ID != "PaymentsSecrets.STRIPE_KEY" || strings.Join(env.Secrets[0].Readers, ",") != tc.reads {
				t.Errorf("secrets = %+v, want the key read by %s", env.Secrets, tc.reads)
			}
		})
	}

	s, services := jobShop()
	service(services, "shop-db").FieldKeys = "VaultKeys.SHOP_DB_FIELD_KEYS"
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnrealizable,
		"job "+stacktest.ShipOrdersJob+" serves shop-orders, whose database shop-db keeps Encrypted<T> fields under the keys of the secret VaultKeys.SHOP_DB_FIELD_KEYS, and it reads no such secret",
		"give shop-orders's @envVars class that field")
	mustFail(t, errs, stack.CodeUnrealizable, "server shop-api serves shop-api, whose database shop-db")
}
