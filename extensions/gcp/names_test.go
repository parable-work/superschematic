package gcp

import (
	"encoding/json"
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
