package shop_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestStackDevRunsTheStorefront runs storefront-stack's Dev, the stack whose
// Staging runs the storefront alone on Cloudflare Workers (D64), with
// `superschematic stack dev`: the storefront's server is the same
// generated main.ts on Bun, beside the fetch.ts and bundle.ts its Workers
// environment adds, and needs no container. A shopper puts a green tea in
// a cart and a viewer reads it back.
//
// scripts/check.sh sets ACME_SHOP_SUPERSCHEMATIC to the binary. Without it,
// or without bun, the test skips.
func TestStackDevRunsTheStorefront(t *testing.T) {
	binary := os.Getenv("ACME_SHOP_SUPERSCHEMATIC")
	if binary == "" {
		t.Skip("set ACME_SHOP_SUPERSCHEMATIC to the superschematic binary, as scripts/check.sh does, to run the stack")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not available to run the storefront's server")
	}
	dev := startStackDev(t, binary, "schemas/services/storefront-stack")
	env := dev.waitUntilRunning(t, filepath.Join("..", "schemas", "dist"), "storefront-stack", "shop-storefront")
	if len(env.containers) != 0 {
		t.Errorf("the storefront's Dev runs containers %v", env.containers)
	}
	for _, file := range []string{"main.ts", "fetch.ts", "bundle.ts"} {
		if _, err := os.Stat(filepath.Join("..", "schemas", "dist", "server", "storefront-stack", "shop-storefront", file)); err != nil {
			t.Errorf("the storefront's package has no %s: %v", file, err)
		}
	}

	cart := fmt.Sprintf("%s/api/carts/00000000-0000-4000-8000-%012d", env.servers["shop-storefront"], time.Now().UnixNano()%1_000_000_000_000)
	if status, body := call(t, http.MethodPost, cart+"/lines", "shopper-token", `{"sku": "green-tea", "quantity": 1}`); status != http.StatusCreated {
		t.Fatalf("add a cart line: %d %s", status, body)
	}
	status, body := call(t, http.MethodGet, cart, "viewer-token", "")
	var read struct {
		Data struct {
			Total struct {
				AmountCents int `json:"amountCents"`
			} `json:"total"`
		} `json:"data"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &read) != nil || read.Data.Total.AmountCents != 450 {
		t.Fatalf("read the cart: %d %s", status, body)
	}
	dev.stop(t)
}
