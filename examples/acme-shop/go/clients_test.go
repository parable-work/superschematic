package shop_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "example.com/acme/sdk/go/shop-orders"
	types "example.com/acme/types/go/shop-orders"
	scalars "github.com/parable-work/superscalar/go"
)

// ordersClientGo makes the same calls as the TypeScript, Python and Rust
// clients, through the generated Go SDK, and prints the same lines. token
// is the shopper's bearer token.
func ordersClientGo(ctx context.Context, baseURL, token string) (string, error) {
	anonymous, err := sdk.New(sdk.SDKConfig{BaseURL: baseURL})
	if err != nil {
		return "", err
	}
	shopper, err := sdk.New(sdk.SDKConfig{BaseURL: baseURL, Auth: &sdk.AuthConfig{Token: token}})
	if err != nil {
		return "", err
	}
	product, _ := scalars.ParseUUID("00000000-0000-4000-8000-0000000000b1")
	var out strings.Builder

	reviews, err := anonymous.ProductReviewsNamespace.ListReviews(ctx, product.String(), nil)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&out, "reviews: %d\n", len(reviews))

	_, err = anonymous.ProductReviewsNamespace.WriteReview(ctx, product.String(), types.WriteReviewInput{
		Rating: 5, Title: "Lovely", Body: "Smoky and smooth.",
	})
	fmt.Fprintf(&out, "write a review without a token: %s\n", status(err))

	_, err = shopper.OrderNamespace.PlaceOrder(ctx, types.PlaceOrderInput{
		Lines:           []types.PlaceOrderLine{{ProductId: product, Quantity: 2}},
		ShippingAddress: address,
	})
	fmt.Fprintf(&out, "order a product that does not exist: %s\n", status(err))

	_, err = shopper.OrderNamespace.GetOrder(ctx, "00000000-0000-4000-8000-0000000000c1")
	fmt.Fprintf(&out, "get a missing order: %s\n", status(err))
	return out.String(), nil
}

// status reports the HTTP status of a failed call. A 401 or 403 comes back as
// an *AuthenticationError or *AuthorizationError, which embed *APIError; each
// is its own type to errors.As.
func status(err error) string {
	var (
		apiErr          *sdk.APIError
		unauthenticated *sdk.AuthenticationError
		forbidden       *sdk.AuthorizationError
	)
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &unauthenticated):
		return fmt.Sprint(unauthenticated.StatusCode)
	case errors.As(err, &forbidden):
		return fmt.Sprint(forbidden.StatusCode)
	case errors.As(err, &apiErr):
		return fmt.Sprint(apiErr.StatusCode)
	}
	return err.Error()
}

// clientsSee is what every client prints against shop-orders, served in
// Go or in Rust.
const clientsSee = "reviews: 0\nwrite a review without a token: 401\norder a product that does not exist: 400\nget a missing order: 404\n"

// Every generated SDK calls the same Go server and sees the same answers,
// as a shopper signed in through shop-api's login. scripts/check.sh sets
// ACME_SHOP_CLIENTS=1 once it has built and linked the TypeScript, Python
// and Rust clients; without it only the Go client runs.
func TestEverySDKCallsTheGoServer(t *testing.T) {
	server, token := ordersServer(t, "orders")
	runClients(t, server.URL, token)
}

// Every generated SDK calls shop-orders served by the generated Rust server
// (rust-server/) and sees what it sees from the Go server, as a shopper
// signed in through shop-api's login. scripts/check.sh builds the server
// and sets ACME_SHOP_RUST_SERVER to its binary. The shop's users are in a
// SQLite database of shop-db's identity tables, which the Rust server reads
// too: the shopper signs in on shop-api, served in Go over it, and the
// Rust server, given the database in ACME_SHOP_DATABASE, prints the URL it
// serves on and authenticates the shopper's session from it.
func TestEverySDKCallsTheRustServer(t *testing.T) {
	binary := os.Getenv("ACME_SHOP_RUST_SERVER")
	if binary == "" {
		t.Skip("set ACME_SHOP_RUST_SERVER to rust-server's binary, as scripts/check.sh does, to run the clients against it")
	}
	database := filepath.Join(t.TempDir(), "shop.db")
	u := newSQLiteUsers(t, database)
	u.add(t, shopperLogin, "Grace Hopper", shopperPassword, "orders")
	token := signIn(t, productsClient(t, productsServer(t, u)), shopperLogin, shopperPassword)

	server := exec.Command(binary)
	server.Env = append(os.Environ(), "ACME_SHOP_DATABASE="+database)
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	baseURL, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("the Rust server printed no URL: %v", err)
	}
	runClients(t, strings.TrimSpace(baseURL), token)
}

// runClients runs the Go client against baseURL as the shopper whose
// bearer token is token, then, with ACME_SHOP_CLIENTS=1, the TypeScript,
// Python and Rust clients, and checks that each prints clientsSee.
func runClients(t *testing.T, baseURL, token string) {
	t.Helper()
	got, err := ordersClientGo(context.Background(), baseURL, token)
	if err != nil {
		t.Fatal(err)
	}
	if got != clientsSee {
		t.Fatalf("the Go client printed:\n%s", got)
	}
	if os.Getenv("ACME_SHOP_CLIENTS") != "1" {
		t.Skip("set ACME_SHOP_CLIENTS=1, as scripts/check.sh does, to run the other languages' clients")
	}

	clients := map[string][]string{
		"typescript": {"bun", "run", "../typescript/clients/orders-client.ts", baseURL, token},
		"python":     {os.Getenv("ACME_SHOP_PYTHON"), "-B", "../python/orders_client.py", baseURL, token},
		"rust":       {rustClient(), baseURL, token},
	}
	for language, argv := range clients {
		t.Run(language, func(t *testing.T) {
			command := exec.Command(argv[0], argv[1:]...)
			command.Env = append(os.Environ(), "PYTHONPATH="+os.Getenv("ACME_SHOP_PYTHONPATH"))
			// Only what the client prints is compared; the Python SDK also
			// logs each failed request to stderr.
			var stderr strings.Builder
			command.Stderr = &stderr
			got, err := command.Output()
			if err != nil {
				t.Fatalf("%s client: %v\n%s%s", language, err, got, stderr.String())
			}
			if string(got) != clientsSee {
				t.Fatalf("%s client printed:\n%s\nthe Go client printed:\n%s", language, got, clientsSee)
			}
		})
	}
}

// rustClient is the Rust client's binary: ACME_SHOP_RUST_CLIENT, which
// scripts/check.sh sets from CARGO_TARGET_DIR, or rust/'s own target.
func rustClient() string {
	if binary := os.Getenv("ACME_SHOP_RUST_CLIENT"); binary != "" {
		return binary
	}
	return "../rust/target/debug/acme-shop-orders-client"
}
