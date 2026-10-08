package shop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	orm "example.com/acme/orm/shop-db"
	apisdk "example.com/acme/sdk/go/shop-api"
	"example.com/acme/sdk/go/shop-api/namespaces"
	orderssdk "example.com/acme/sdk/go/shop-orders"
	apitypes "example.com/acme/types/go/shop-api"
	db "example.com/acme/types/go/shop-db"
	orderstypes "example.com/acme/types/go/shop-orders"
	scalars "github.com/parable-work/superscalar/go"
)

// TestStackDevRunsTheShop is milestones 1 and 7 of docs/stack-model.md
// (section 14) on this example. `superschematic stack dev` runs
// shop-stack's Dev environment from examples/acme-shop, as the tutorial
// does: Postgres in a container with shop-db migrated; shop-api and
// shop-orders each on its generated Go entrypoint, built with the
// implementations in go/shop-api and go/shop-orders; and shop-storefront on
// its generated TypeScript entrypoint, which Bun runs with the
// implementation in typescript/shop-storefront (D51). Every connection
// string, URL and port the test uses comes from the environment the build
// resolved. The test waits for each server's /readyz, signs a user in
// through the generated ORM, calls each Go API through its generated Go
// SDK and the storefront over HTTP. Then shop-orders' job ShipOrders, on
// its generated entrypoint with the implementation's NewJobs, ships the
// order placed: once on demand with `superschematic stack run`, and again
// on the every-minute schedule Dev's settings give it, which stack dev runs
// (section 8.7, D52). Last, the test stops the stack as Ctrl-C does, which
// with --remove-database removes the container.
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
	// stack dev installs the Bun workspace of that output root, whose
	// members include the packages in typescript/, which the install links
	// to it. Installing schemas/dist's workspace again links them back,
	// frozen to its committed lockfile, which it leaves as it is.
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join("..", "schemas", "dist", "package.json")); err != nil {
			return
		}
		install := exec.Command("bun", "install", "--frozen-lockfile")
		install.Dir = filepath.Join("..", "schemas", "dist")
		if out, err := install.CombinedOutput(); err != nil {
			t.Errorf("link typescript/ back to schemas/dist: %v\n%s", err, out)
		}
	})
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
	for _, server := range []string{"shop-api", "shop-orders", "shop-storefront"} {
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
	token := signIn(ctx, t, env.database)
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

	// shop-storefront, on Bun: a shopper puts two green teas in a cart,
	// and a viewer reads the cart back. Its implementation keeps carts in
	// memory and knows its callers by static tokens.
	storefront := env.servers["shop-storefront"]
	cart := fmt.Sprintf("%s/api/carts/00000000-0000-4000-8000-%012d", storefront, n%1_000_000_000_000)
	if status, body := call(t, http.MethodPost, cart+"/lines", "shopper-token", `{"sku": "green-tea", "quantity": 2}`); status != http.StatusCreated {
		t.Fatalf("add a cart line: %d %s", status, body)
	}
	status, body := call(t, http.MethodGet, cart, "viewer-token", "")
	var read struct {
		Data struct {
			Lines []struct {
				Sku      string `json:"sku"`
				Quantity int    `json:"quantity"`
			} `json:"lines"`
			Total struct {
				AmountCents int    `json:"amountCents"`
				Currency    string `json:"currency"`
			} `json:"total"`
		} `json:"data"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &read) != nil || len(read.Data.Lines) != 1 || read.Data.Lines[0].Quantity != 2 ||
		read.Data.Total.AmountCents != 900 || read.Data.Total.Currency != "EUR" {
		t.Fatalf("read the cart: %d %s", status, body)
	}
	if status, body := call(t, http.MethodGet, cart, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("read the cart without a token: %d %s, want 401", status, body)
	}

	// shop-orders' job ShipOrders ships every placed order (D52).
	// `superschematic stack run` runs it once, from another terminal,
	// against the environment stack dev runs.
	run := runJob(t, binary, outputRoot)
	for _, want := range []string{
		"job " + shipOrders + ": start the run stack run asked for, try 1 of 2",
		"job " + shipOrders + ": the run stack run asked for, try 1 of 2 succeeded",
		"[" + shipOrders + "] ",
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("stack run printed no %q:\n%s", want, run)
		}
	}
	if got := orderStatus(ctx, t, orders, order.Id); got != orderstypes.OrderStatus_Shipped {
		t.Fatalf("after stack run, order %s is %s, want shipped", order.Id, got)
	}

	// Dev's settings run the job every minute, so stack dev ships an order
	// placed now within about one.
	if !strings.Contains(output.String(), "job "+shipOrders+" runs on * * * * * (UTC)") {
		t.Fatalf("stack dev did not schedule %s", shipOrders)
	}
	next, err := orders.OrderNamespace.PlaceOrder(ctx, orderstypes.PlaceOrderInput{
		Lines:           []orderstypes.PlaceOrderLine{{ProductId: product.Id, Quantity: 1}},
		ShippingAddress: address,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	deadline = time.Now().Add(3 * time.Minute)
	for orderStatus(ctx, t, orders, next.Id) != orderstypes.OrderStatus_Shipped {
		if time.Now().After(deadline) {
			t.Fatalf("stack dev's schedule did not ship order %s in 3 minutes", next.Id)
		}
		time.Sleep(time.Second)
	}
	for _, want := range []string{"job " + shipOrders + ": start the run due at ", "[" + shipOrders + "] "} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stack dev printed no %q", want)
		}
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

// shipOrders is the deployable of shop-orders' job ShipOrders: the API's
// name, then the job's class in kebab case.
const shipOrders = "shop-orders-ship-orders"

// runJob runs shop-orders' job once with `superschematic stack run`, from
// the example's directory as a person would, and returns what it printed.
// A run from stack dev's every-minute schedule may hold the job while it
// goes on; then stack run refuses, and runJob tries again.
func runJob(t *testing.T, binary, outputRoot string) string {
	t.Helper()
	for try := 1; ; try++ {
		cmd := exec.Command(binary, "stack", "run", "Dev", shipOrders, "--out", outputRoot)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out)
		}
		if try < 5 && strings.Contains(string(out), "goes on, from stack dev's schedule") {
			time.Sleep(time.Second)
			continue
		}
		t.Fatalf("stack run: %v\n%s", err, out)
	}
}

// orderStatus reads an order's status through the generated SDK.
func orderStatus(ctx context.Context, t *testing.T, orders *orderssdk.ShopOrdersSDK, id orderstypes.IdentityUUID) orderstypes.OrderStatus {
	t.Helper()
	order, err := orders.OrderNamespace.GetOrder(ctx, id.String())
	if err != nil {
		t.Fatalf("GetOrder %s: %v", id, err)
	}
	return order.Status
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

// signIn creates a user and a session of an hour in shop-db, through the
// generated ORM, as the shop's sign-in would, and returns the session's
// bearer token: its jti (shop.SessionToken).
func signIn(ctx context.Context, t *testing.T, databaseURL string) string {
	t.Helper()
	database, err := orm.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	user, err := database.GetUserRepository().CreateOne(ctx, &db.User{
		Email: db.ContactEmail(fmt.Sprintf("ada.%d@example.com", time.Now().UnixNano())),
		Name:  "Ada Lovelace",
	})
	if err != nil {
		t.Fatalf("create the user: %v", err)
	}
	jti := scalars.NewUUID()
	_, err = database.GetSessionRepository().CreateOne(ctx, &db.Session{
		Jti:       jti,
		User:      db.User{Id: user.Id},
		ExpiresAt: db.TemporalDateTime(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("create the session: %v", err)
	}
	return jti.String()
}

func containsProduct(products []apitypes.ProductView, id apitypes.IdentityUUID) bool {
	for _, p := range products {
		if p.Id == id {
			return true
		}
	}
	return false
}

// call sends a request with a bearer token, unless token is empty, and a
// JSON body, unless body is empty, and returns the status and the body of
// the answer.
func call(t *testing.T, method, url, token, body string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, answer
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
