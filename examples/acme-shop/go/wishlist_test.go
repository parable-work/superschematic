package shop_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStackDevRunsTheWishlist runs wishlist-stack's Dev, the stack whose
// Staging runs the wishlist on Cloudflare Workers over D1 (D64), with
// `superschematic stack dev`: shop-wishlist-db lists sqlite first, so its
// database is a SQLite file under the environment's state directory, which
// stack dev creates and migrates from shop-wishlist-db's SQLite plan with
// superschematic-migrate, and no container runs. The wishlist's server is
// its generated main.ts on Bun, beside the fetch.ts and bundle.ts its
// Workers environment adds, and opens the file at the sqlite: URL its edge
// derives as Deps.db, a SqlDatabase, D1's interface. A shopper keeps two
// teas, changes a note, and drops one; --remove-data removes the file.
//
// scripts/check.sh sets ACME_SHOP_SUPERSCHEMATIC to the binary and
// SUPERSCHEMATIC_MIGRATE to the migration runner. Without the binary, bun
// or the runner, the test skips.
func TestStackDevRunsTheWishlist(t *testing.T) {
	binary := os.Getenv("ACME_SHOP_SUPERSCHEMATIC")
	if binary == "" {
		t.Skip("set ACME_SHOP_SUPERSCHEMATIC to the superschematic binary, as scripts/check.sh does, to run the stack")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not available to run the wishlist's server")
	}
	if _, err := exec.LookPath("superschematic-migrate"); err != nil && os.Getenv("SUPERSCHEMATIC_MIGRATE") == "" {
		t.Skip("set SUPERSCHEMATIC_MIGRATE to the migration runner, as scripts/check.sh does, to migrate the wishlist's SQLite file")
	}
	dev := startStackDev(t, binary, "schemas/services/wishlist-stack")
	env := dev.waitUntilRunning(t, filepath.Join("..", "schemas", "dist"), "wishlist-stack", "shop-wishlist")
	if len(env.containers) != 0 {
		t.Errorf("the wishlist's Dev runs containers %v", env.containers)
	}
	for _, file := range []string{"main.ts", "fetch.ts", "bundle.ts"} {
		if _, err := os.Stat(filepath.Join("..", "schemas", "dist", "server", "wishlist-stack", "shop-wishlist", file)); err != nil {
			t.Errorf("the wishlist's package has no %s: %v", file, err)
		}
	}
	database := filepath.Join("..", "schemas", ".superschematic", "local", "wishlist-stack", "Dev", "databases", "shop_wishlist_db.sqlite")
	if _, err := os.Stat(database); err != nil {
		t.Fatalf("stack dev made no SQLite file for shop-wishlist-db: %v", err)
	}
	output := dev.output.String()
	for _, want := range []string{"create database shop-wishlist-db: ", "migrate shop-wishlist-db: ", "  database shop-wishlist-db"} {
		if !strings.Contains(output, want) {
			t.Errorf("stack dev printed no %q", want)
		}
	}

	shopper := fmt.Sprintf("%s/api/wishlists/ada-%d", env.servers["shop-wishlist"], time.Now().UnixNano()%1_000_000_000)
	for _, keep := range []struct{ sku, note string }{{"green-tea", "for the mornings"}, {"oolong", ""}} {
		body := "{}"
		if keep.note != "" {
			body = fmt.Sprintf(`{"note": %q}`, keep.note)
		}
		if status, answer := call(t, http.MethodPut, shopper+"/items/"+keep.sku, "", body); status != http.StatusOK {
			t.Fatalf("keep %s: %d %s", keep.sku, status, answer)
		}
	}
	if status, answer := call(t, http.MethodPut, shopper+"/items/oolong", "", `{"note": "a gift"}`); status != http.StatusOK {
		t.Fatalf("change oolong's note: %d %s", status, answer)
	}
	type entry struct {
		SKU  string  `json:"sku"`
		Note *string `json:"note"`
	}
	read := func() []entry {
		t.Helper()
		status, answer := call(t, http.MethodGet, shopper, "", "")
		var view struct {
			Data struct {
				Items []entry `json:"items"`
			} `json:"data"`
		}
		if status != http.StatusOK || json.Unmarshal(answer, &view) != nil {
			t.Fatalf("read the wishlist: %d %s", status, answer)
		}
		return view.Data.Items
	}
	note := func(e entry) string {
		if e.Note == nil {
			return "<none>"
		}
		return *e.Note
	}
	items := read()
	if len(items) != 2 || items[0].SKU != "green-tea" || note(items[0]) != "for the mornings" || items[1].SKU != "oolong" || note(items[1]) != "a gift" {
		t.Fatalf("the wishlist holds %+v, want green-tea for the mornings and oolong, a gift", items)
	}
	if status, answer := call(t, http.MethodDelete, shopper+"/items/green-tea", "", ""); status != http.StatusOK {
		t.Fatalf("drop green-tea: %d %s", status, answer)
	}
	if items := read(); len(items) != 1 || items[0].SKU != "oolong" {
		t.Fatalf("after the drop the wishlist holds %+v, want oolong alone", items)
	}

	dev.stop(t)
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Errorf("--remove-data left the SQLite file: %v", err)
	}
}
