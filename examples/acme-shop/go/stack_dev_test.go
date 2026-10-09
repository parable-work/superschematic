package shop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apisdk "example.com/acme/sdk/go/shop-api"
	"example.com/acme/sdk/go/shop-api/namespaces"
	orderssdk "example.com/acme/sdk/go/shop-orders"
	apitypes "example.com/acme/types/go/shop-api"
	db "example.com/acme/types/go/shop-db"
	orderstypes "example.com/acme/types/go/shop-orders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
)

// TestStackDevRunsTheShop is milestone 1 of docs/stack-model.md (section
// 14) on this example. `superschematic stack dev` runs shop-stack's Dev
// environment from examples/acme-shop, as the tutorial does: Postgres in a
// container with shop-db migrated, and shop-api and shop-orders each on its
// generated entrypoint, built with the implementations in go/shop-api and
// go/shop-orders. Every connection string, URL and port the test uses comes
// from the environment the build resolved. The test waits for each server's
// /readyz, creates a user in shop-db with the identity runtime's store, as
// staff would through the administration routes, signs them in through
// shop-api's generated login, calls each API through its generated Go SDK
// with the session, then stops the stack as Ctrl-C does, which with
// --remove-database removes the container.
//
// scripts/check.sh sets ACME_SHOP_SUPERSCHEMATIC to the core binary and
// SUPERSCHEMATIC_MIGRATE to the migration runner. Without the binary, or
// without Docker, the test skips.
func TestStackDevRunsTheShop(t *testing.T) {
	binary := os.Getenv("ACME_SHOP_SUPERSCHEMATIC")
	if binary == "" {
		t.Skip("set ACME_SHOP_SUPERSCHEMATIC to the superschematic binary, as scripts/check.sh does, to run the stack")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is not available: %v", err)
	}

	// The build goes to an output root of the test's own, so schemas/dist,
	// which scripts/check.sh compares with testdata/generated/, is left as
	// build-all wrote it.
	outputRoot := t.TempDir()
	output := &lockedBuffer{}
	stack := exec.Command(binary, "stack", "dev", "--remove-database", "--out", outputRoot)
	stack.Dir = ".."
	stack.Stdout, stack.Stderr = output, output
	if err := stack.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- stack.Wait() }()
	// On a failure, stop stack dev as Ctrl-C does, then remove the
	// container whatever it left.
	stopped, container := false, ""
	t.Cleanup(func() {
		if !stopped {
			_ = stack.Process.Signal(os.Interrupt)
			select {
			case <-exited:
			case <-time.After(time.Minute):
				_ = stack.Process.Kill()
			}
		}
		if container != "" {
			_ = exec.Command("docker", "rm", "--force", "--volumes", container).Run()
		}
		if t.Failed() {
			t.Logf("stack dev printed:\n%s", output)
		}
	})

	// stack dev prints the summary once every server answers /readyz.
	deadline := time.Now().Add(5 * time.Minute)
	for !strings.Contains(output.String(), "is running:") {
		select {
		case err := <-exited:
			stopped = true
			t.Fatalf("stack dev exited before the stack ran: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the stack did not come up in 5 minutes")
		}
		time.Sleep(200 * time.Millisecond)
	}

	env := readEnvironment(t, filepath.Join(outputRoot, "stack", "shop-stack", "Dev", "environment.json"))
	container = env.container
	for _, server := range []string{"shop-api", "shop-orders"} {
		if env.servers[server] == "" {
			t.Fatalf("environment Dev has no server %s: %v", server, env.servers)
		}
	}
	for server, url := range env.servers {
		if status := get(t, url+"/readyz"); status != http.StatusOK {
			t.Fatalf("%s answered /readyz %d", server, status)
		}
	}

	ctx := context.Background()
	token := signInOnTheStack(ctx, t, env.database, env.servers["shop-api"])
	n := time.Now().UnixNano()

	// shop-api: staff add a product, and it is listed.
	products, err := apisdk.New(apisdk.SDKConfig{BaseURL: env.servers["shop-api"], Auth: &apisdk.AuthConfig{Token: token}})
	if err != nil {
		t.Fatal(err)
	}
	product, err := products.ProductNamespace.CreateProduct(ctx, apitypes.CreateProductInput{
		Sku:        apitypes.IdentitySlug(fmt.Sprintf("green-tea-%d", n)),
		Name:       "Green tea",
		PriceCents: 450,
	})
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	listed, err := products.ProductNamespace.ListProducts(ctx, &namespaces.ProductListProductsQueryParams{InStock: true})
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if !containsProduct(listed, product.Id) {
		t.Fatalf("ListProducts returned %+v, without %s", listed, product.Id)
	}

	// shop-orders: a shopper orders two of it, in one transaction over
	// the order and its lines.
	orders, err := orderssdk.New(orderssdk.SDKConfig{BaseURL: env.servers["shop-orders"], Auth: &orderssdk.AuthConfig{Token: token}})
	if err != nil {
		t.Fatal(err)
	}
	order, err := orders.OrderNamespace.PlaceOrder(ctx, orderstypes.PlaceOrderInput{
		Lines:           []orderstypes.PlaceOrderLine{{ProductId: product.Id, Quantity: 2}},
		ShippingAddress: address,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if order.Status != orderstypes.OrderStatus_Placed || order.TotalCents != 900 || len(order.Lines) != 1 {
		t.Fatalf("PlaceOrder returned %+v", order)
	}

	// Ctrl-C stops the servers, then removes the container and its data.
	stopped = true
	if err := stack.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("stack dev exited with %v", err)
		}
	case <-time.After(2 * time.Minute):
		_ = stack.Process.Kill()
		t.Fatal("stack dev did not stop in 2 minutes")
	}
	if !strings.Contains(output.String(), "removed container "+env.container) {
		t.Fatalf("stack dev did not remove container %s", env.container)
	}
	if exec.Command("docker", "container", "inspect", env.container).Run() == nil {
		t.Fatalf("container %s outlived --remove-database", env.container)
	}
}

// environment is what the test reads from the environment the build
// resolved: each server's URL, shop-db's connection string and the
// Postgres container's name.
type environment struct {
	servers   map[string]string
	database  string
	container string
}

func readEnvironment(t *testing.T, path string) environment {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var resolved struct {
		Deployables []struct {
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Address string `json:"address"`
		} `json:"deployables"`
		Resources struct {
			Resources []struct {
				Type       string         `json:"type"`
				Properties map[string]any `json:"properties"`
			} `json:"resources"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &resolved); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	env := environment{servers: map[string]string{}}
	for _, d := range resolved.Deployables {
		if d.Kind == "server" {
			env.servers[d.Name] = d.Address
		}
	}
	for _, r := range resolved.Resources.Resources {
		switch {
		case r.Type == "local:docker/container:Container":
			env.container, _ = r.Properties["name"].(string)
		case r.Type == "local:postgres/database:Database" && r.Properties["service"] == "shop-db":
			env.database, _ = r.Properties["url"].(string)
		}
	}
	if env.database == "" || env.container == "" {
		t.Fatalf("%s names no shop-db database or no container", path)
	}
	return env
}

// signInOnTheStack creates a user in shop-db with a role that holds
// products and orders, through the identity runtime's store over the
// database, as staff would through shop-api's administration routes; signs
// them in through shop-api's generated login with a bearer session, and
// returns its token. It also signs in with a cookie session, which the
// local target's servers, on plain HTTP, write as session without Secure.
func signInOnTheStack(ctx context.Context, t *testing.T, databaseURL, shopAPI string) string {
	t.Helper()
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*config)
	defer database.Close()
	store, err := identity.NewSQLStore(database, identity.Postgres, []byte(db.IdentityDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	n := time.Now().UnixNano()
	login, password := fmt.Sprintf("ada.%d@example.com", n), "ada's password"
	hash, err := identity.HashPassword(password, identity.Config{}.Argon2Params())
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, identity.NewUser{Login: login, Name: "Ada Lovelace", PasswordHash: hash, At: time.Now()})
	if err != nil {
		t.Fatalf("create the user: %v", err)
	}
	role, err := store.CreateRole(ctx, fmt.Sprintf("staff-%d", n), []string{"products", "orders"})
	if err != nil {
		t.Fatalf("create the role: %v", err)
	}
	if err := store.GrantRole(ctx, user.ID, role.ID, time.Now()); err != nil {
		t.Fatalf("grant the role: %v", err)
	}

	client, err := apisdk.New(apisdk.SDKConfig{BaseURL: shopAPI})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.AccountNamespace.Login(ctx, apitypes.LoginInput{Login: apitypes.ContactEmail(login), Password: apitypes.AuthPassword(password)})
	if err != nil || result.Token == "" {
		t.Fatalf("login: %+v, %v", result, err)
	}

	request, err := http.NewRequest(http.MethodPost, shopAPI+"/api/auth/login",
		strings.NewReader(fmt.Sprintf(`{"login": %q, "password": %q, "session": "cookie"}`, login, password)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if cookies := response.Cookies(); response.StatusCode != http.StatusOK || len(cookies) != 1 || cookies[0].Name != identity.PlainCookieName || cookies[0].Secure {
		t.Fatalf("a cookie login on the local target answered %d with %v, want the session cookie without Secure", response.StatusCode, cookies)
	}
	return result.Token
}

func containsProduct(products []apitypes.ProductView, id apitypes.IdentityUUID) bool {
	for _, p := range products {
		if p.Id == id {
			return true
		}
	}
	return false
}

func get(t *testing.T, url string) int {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

// lockedBuffer is stack dev's output, which its process writes while the
// test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
