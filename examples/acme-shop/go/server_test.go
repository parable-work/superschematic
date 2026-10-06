package shop_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	shopapi "example.com/acme/api/shop-api"
	orm "example.com/acme/orm/shop-db"
	sdk "example.com/acme/sdk/go/shop-api"
	"example.com/acme/sdk/go/shop-api/namespaces"
	"example.com/acme/shop"
	shopapiimpl "example.com/acme/shop/shop-api"
	types "example.com/acme/types/go/shop-api"
	"github.com/go-chi/chi/v5"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"go.uber.org/zap"
)

// verifyJWT stands in for your identity provider: it accepts one token and
// returns the JWT id of its session.
func verifyJWT(_ context.Context, token string) (string, error) {
	if token != "token-1" {
		return "", errors.New("invalid token")
	}
	return "session-1", nil
}

type fakeSessions struct{}

func (fakeSessions) FindByJTI(_ context.Context, jti string) (session.Record, error) {
	if jti != "session-1" {
		return session.Record{}, session.ErrNotFound
	}
	return session.Record{ID: jti, PrincipalID: "00000000-0000-4000-8000-0000000000a1", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type fakePrincipals struct{}

func (fakePrincipals) GetByID(_ context.Context, id string) (session.Principal, error) {
	return session.Principal{ID: id, Name: "Ada"}, nil
}

// staffRoles grants every principal the products permission, which covers
// products.read and products.write.
type staffRoles struct{}

func (staffRoles) ListRolesForPrincipal(context.Context, string) ([]session.Role, error) {
	return []session.Role{{ID: "staff", Name: "Staff", Permissions: []string{"products"}}}, nil
}

// productsServer serves shop-api in-process, as its server's generated
// entrypoint does: the implementation New builds from its Deps, and the
// generated routes on a router. The ORM is the generated no-op database,
// so no Postgres is needed: lists are empty, a get is not found, and a
// create returns what it was given. auth stands in for the stores in
// shop-db.
func productsServer(t *testing.T, auth shop.Auth) *httptest.Server {
	t.Helper()
	deps := shopapi.Deps{DB: orm.NewNoOpDatabase(), Logger: zap.NewNop()}
	implementations, err := shopapiimpl.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	err = shopapi.RegisterRoutes(router, shopapi.Config{
		DB:              deps.DB,
		Logger:          deps.Logger,
		AuthMiddleware:  auth.Middleware,
		Implementations: implementations,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

// The generated Go SDK calls the generated Go server, which calls Products.
func TestTheSDKCallsTheGeneratedServer(t *testing.T) {
	server := productsServer(t, shop.Auth{
		Validate:   verifyJWT,
		Sessions:   fakeSessions{},
		Principals: fakePrincipals{},
		Roles:      staffRoles{},
	})
	ctx := context.Background()

	client, err := sdk.New(sdk.SDKConfig{
		BaseURL: server.URL,
		Auth:    &sdk.AuthConfig{Token: "token-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := client.ProductNamespace.CreateProduct(ctx, types.CreateProductInput{
		Sku:        "green-tea",
		Name:       "Green tea",
		PriceCents: 450,
	})
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if created.Sku != "green-tea" || created.PriceCents != 450 || !created.InStock {
		t.Fatalf("CreateProduct returned %+v", created)
	}

	products, err := client.ProductNamespace.ListProducts(ctx, &namespaces.ProductListProductsQueryParams{InStock: true})
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if len(products) != 0 {
		t.Fatalf("ListProducts returned %d products from the no-op database", len(products))
	}

	_, err = client.ProductNamespace.GetProduct(ctx, "00000000-0000-4000-8000-000000000001")
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Fatalf("GetProduct of a missing product: %v", err)
	}

	// The SDK checks the input before it sends it: priceCents has { min: 0 }.
	_, err = client.ProductNamespace.CreateProduct(ctx, types.CreateProductInput{Sku: "tea", Name: "Tea", PriceCents: -1})
	var invalid *sdk.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("CreateProduct with a negative price: %v", err)
	}

	anonymous, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = anonymous.ProductNamespace.ListProducts(ctx, nil)
	var unauthenticated *sdk.AuthenticationError
	if !errors.As(err, &unauthenticated) {
		t.Fatalf("ListProducts without a token: %v", err)
	}
}

// The server checks what it receives as the SDK checks what it sends, and
// serves its OpenAPI document without a caller.
func TestTheServerValidatesAndDescribesItself(t *testing.T) {
	server := productsServer(t, shop.Auth{
		Validate:   verifyJWT,
		Sessions:   fakeSessions{},
		Principals: fakePrincipals{},
		Roles:      staffRoles{},
	})

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/products",
		strings.NewReader(`{"sku": "tea", "name": "Tea", "priceCents": -1}`))
	request.Header.Set("Authorization", "Bearer token-1")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/products with a negative price answered %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/openapi.json answered %d", response.StatusCode)
	}
}
