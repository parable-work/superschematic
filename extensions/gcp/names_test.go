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
		err := checkLength("id", c.name, 6, 30)
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
