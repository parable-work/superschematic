package gcp

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

func TestKebab(t *testing.T) {
	for in, want := range map[string]string{
		"Orders":        "orders",
		"shop-api":      "shop-api",
		"shop_db":       "shop-db",
		"ShopAPIServer": "shop-api-server",
		"Shop2Go":       "shop2-go",
		"API":           "api",
	} {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestSuffixed(t *testing.T) {
	env := registry.StackEnvironment{Parameters: []string{"pr", "run"}}
	got, err := json.Marshal(suffixed(env, "shop_db", "_"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"$concat":["shop_db_pr",{"$parameter":"pr"},"_run",{"$parameter":"run"}]}`; string(got) != want {
		t.Errorf("suffixed = %s, want %s", got, want)
	}
	if got := suffixed(registry.StackEnvironment{}, "shop-api", "-"); got != "shop-api" {
		t.Errorf("suffixed without parameters = %v", got)
	}
}

func TestCheckLength(t *testing.T) {
	for _, c := range []struct {
		name any
		want string
	}{
		{"orders", ""},
		{"api", "at least 6"},
		{strings.Repeat("a", 31), "allows 30"},
		{ir.Concat{"shop-api-pr", ir.Parameter("pr")}, ""},
		{ir.Concat{strings.Repeat("a", 30), ir.Parameter("pr")}, "before its parameter values"},
	} {
		err := checkLength("id", c.name, 6, 30, "the deployable")
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("checkLength(%v) = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestSecretName(t *testing.T) {
	if got := secretName("Shop", "PaymentsSecrets.STRIPE_KEY"); got != "Shop-PaymentsSecrets-STRIPE_KEY" {
		t.Errorf("secretName = %s", got)
	}
}

// TestEngineRole: the role that owns the engine's schema is named after
// its database, unique on the instance as the database's name is, and a
// name past Postgres's 63 bytes keeps a hash of the whole name.
func TestEngineRole(t *testing.T) {
	if got := engineRole("shop_db_pr7"); got != "shop_db_pr7_engine" {
		t.Errorf("engineRole = %s", got)
	}
	long := strings.Repeat("d", 60)
	a, b := engineRole(long), engineRole(long[:59]+"e")
	if len(a) != 63 || len(b) != 63 || a == b || !strings.HasSuffix(a, "_engine") || !strings.HasPrefix(a, long[:46]+"_") {
		t.Errorf("long names: %s, %s", a, b)
	}
	if got := engineRole(strings.Repeat("d", 56)); got != strings.Repeat("d", 56)+"_engine" {
		t.Errorf("a name that fits: %s", got)
	}
}

// TestShorten: a derived name that fits is itself; a longer one is a
// prefix of it and seven hex digits of its hash, within the limit, the
// same each time and apart from every other name's, starting with the
// name's letter and ending with a letter or a digit (D97).
func TestShorten(t *testing.T) {
	if got := shorten("finances-api-sync-items", 30, "-"); got != "finances-api-sync-items" {
		t.Errorf("a name that fits = %s", got)
	}
	if got := shorten(strings.Repeat("a", 30), 30, "-"); got != strings.Repeat("a", 30) {
		t.Errorf("a name of the limit = %s", got)
	}
	long := "finances-api-sync-all-plaid-items"
	got := shorten(long, 30, "-")
	if !regexp.MustCompile(`^finances-api-sync-all-[0-9a-f]{7}$`).MatchString(got) {
		t.Errorf("shorten(%s) = %s, want its prefix, less the hyphen it ends with, a hyphen and seven hex digits", long, got)
	}
	if again := shorten(long, 30, "-"); again != got {
		t.Errorf("shorten is not stable: %s, then %s", got, again)
	}
	account := regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])$`)
	seen := map[string]string{}
	for _, name := range []string{
		long,
		"finances-api-sync-all-plaid-itemz",
		"finances-api-sync-all-plaid-items-again",
		"finances-api-sync-all-plaid-accounts",
		"orders-fulfillment-and-invoicing-api",
		strings.Repeat("x", 200),
	} {
		short := shorten(name, 30, "-")
		if len(short) > 30 || !account.MatchString(short) {
			t.Errorf("shorten(%s) = %s, which is no service account id", name, short)
		}
		if other, ok := seen[short]; ok {
			t.Errorf("%s and %s both shorten to %s", name, other, short)
		}
		seen[short] = name
	}
	if got := shorten("finances_db_line_item_embeddings_by_model_and_text_of_the_ledger", 63, "_"); len(got) != 63 || !strings.HasPrefix(got, "finances_db_line_item_") || strings.Contains(got, "-") {
		t.Errorf("a database's name = %s, want underscores within 63 characters", got)
	}
}

// TestFitted: a deployable's name is shortened only where it is refused
// without, and under a parameter leaves parameterRoom characters for the
// values; a parameter whose name leaves no room keeps the name, which
// checkLength refuses (D97).
func TestFitted(t *testing.T) {
	plain := registry.StackEnvironment{}
	if got, short := fitted(plain, "shop-orders-ship-orders", "-", 30); got != "shop-orders-ship-orders" || short {
		t.Errorf("a name that fits = %v, %v", got, short)
	}
	got, short := fitted(plain, "finances-api-sync-all-plaid-items", "-", 30)
	if name, _ := got.(string); !short || len(name) > 30 || checkLength("id", got, 6, 30, "") != nil {
		t.Errorf("a long name = %v, %v", got, short)
	}

	pr := registry.StackEnvironment{Parameters: []string{"pr"}}
	if got, short := fitted(pr, strings.Repeat("a", 26), "-", 30); short || mustJSON(t, got) != `{"$concat":["`+strings.Repeat("a", 26)+`-pr",{"$parameter":"pr"}]}` {
		t.Errorf("a member's name that checkLength passes = %s, %v", mustJSON(t, got), short)
	}
	got, short = fitted(pr, "finances-api-sync-all-plaid-items", "-", 30)
	n, refs := literalLength(got)
	if !short || !refs || n+parameterRoom > 30 || checkLength("id", got, 6, 30, "") != nil {
		t.Errorf("a member's long name = %s, %v: %d characters before the values", mustJSON(t, got), short, n)
	}
	if again, _ := fitted(pr, "finances-api-sync-all-plaid-items", "-", 30); mustJSON(t, again) != mustJSON(t, got) {
		t.Error("fitted is not stable under a parameter")
	}

	long := registry.StackEnvironment{Parameters: []string{"pullrequestnumber", "attempt"}}
	got, short = fitted(long, "finances-api-sync-all-plaid-items", "-", 30)
	if short || checkLength("id", got, 6, 30, "the job's class or its API") == nil {
		t.Errorf("parameters that leave no room = %s, %v, want the name kept and refused", mustJSON(t, got), short)
	}
}

// TestLabelValue: a label holds the deployable's name, its other
// characters made hyphens, shortened alike past 63 characters.
func TestLabelValue(t *testing.T) {
	if got := labelValue("finances-api-sync-all-plaid-items"); got != "finances-api-sync-all-plaid-items" {
		t.Errorf("labelValue = %s", got)
	}
	if got := labelValue("shop.api_x"); got != "shop-api_x" {
		t.Errorf("labelValue of foreign characters = %s", got)
	}
	if got := labelValue(strings.Repeat("ab", 40)); len(got) != 63 {
		t.Errorf("labelValue of 80 characters = %s", got)
	}
}

// TestWorkloadName: a default deployable's name is fit to its account id;
// a declared deployable's, which its author gave, is kept, and
// checkLength refuses it when too long (D97).
func TestWorkloadName(t *testing.T) {
	env := registry.StackEnvironment{}
	job := ir.ResolvedDeployable{Name: "finances-api-sync-all-plaid-items", Kind: ir.DeployableJob}
	if got, short := workloadName(env, job); !short || len(got.(string)) > 30 {
		t.Errorf("a job's long name = %v, %v", got, short)
	}
	declared := ir.ResolvedDeployable{Name: "OrdersFulfillmentAndInvoicingServer", Kind: ir.DeployableServer, Declared: true}
	if got, short := workloadName(env, declared); short || got != "orders-fulfillment-and-invoicing-server" {
		t.Errorf("a declared deployable's name = %v, %v", got, short)
	}
	if got := nameLabels(job, true); mustJSON(t, got) != `{"superschematic-deployable":"finances-api-sync-all-plaid-items"}` {
		t.Errorf("nameLabels = %s", mustJSON(t, got))
	}
	if got := nameLabels(job, false); got != nil {
		t.Errorf("nameLabels of a name that fits = %v", got)
	}
}

// TestCIAccountNames: bootstrap's accounts and pool keep the stack's name
// when it fits, and shorten it alike for every role when the longest
// role's id would pass 30 characters (D97).
func TestCIAccountNames(t *testing.T) {
	if got := accountID("Shop", "deployer"); got != "shop-deployer" {
		t.Errorf("accountID = %s", got)
	}
	if got := workloadIdentityPool("Shop"); got != "shop-github" {
		t.Errorf("workloadIdentityPool = %s", got)
	}
	fits := strings.Repeat("s", 21)
	if got := accountID(fits, "migrator"); got != fits+"-migrator" {
		t.Errorf("a stack of 21 characters = %s", got)
	}
	long := "FinancesProductionStackOfTheHousehold"
	stackPart := strings.TrimSuffix(accountID(long, "deployer"), "-deployer")
	for _, role := range accountRoles {
		id := accountID(long, role)
		if len(id) > 30 || id != stackPart+"-"+role {
			t.Errorf("%s's id = %s, want %s-%s in 30 characters", role, id, stackPart, role)
		}
	}
	if pool := workloadIdentityPool(long); len(pool) > 32 || !strings.HasPrefix(pool, "finances-product") || !strings.HasSuffix(pool, "-github") {
		t.Errorf("workloadIdentityPool = %s", pool)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
