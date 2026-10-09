package shop_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	shopapi "example.com/acme/api/shop-api"
	orm "example.com/acme/orm/shop-db"
	sdk "example.com/acme/sdk/go/shop-api"
	"example.com/acme/sdk/go/shop-api/namespaces"
	shopapiimpl "example.com/acme/shop/shop-api"
	types "example.com/acme/types/go/shop-api"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// staff is a user of the shop who holds products, to read and add them.
const (
	staffLogin    = "ada@example.com"
	staffPassword = "ada's password"

	// storefrontSite is the origin the identity config trusts, which may
	// send cookie requests from across origins.
	storefrontSite = "https://storefront.example.com"
)

// productsServer serves shop-api in-process, as its server's generated
// entrypoint does: the implementation New builds from its Deps, and the
// generated routes on a router, which authenticate with the identity
// service over u's store. The ORM is the generated no-op database, so no
// Postgres is needed: lists are empty, a get is not found, and a create
// returns what it was given.
func productsServer(t *testing.T, u *users) *httptest.Server {
	t.Helper()
	deps := shopapi.Deps{DB: orm.NewNoOpDatabase(), Logger: zap.NewNop()}
	implementations, err := shopapiimpl.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	err = shopapi.RegisterRoutes(router, shopapi.Config{
		DB:              deps.DB,
		Logger:          deps.Logger,
		Identity:        u.shopAPI(t),
		Implementations: implementations,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

// productsClient is a client of shop-api, without a session until signIn
// gives it one.
func productsClient(t *testing.T, server *httptest.Server) *sdk.ShopApiSDK {
	t.Helper()
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// signIn signs in through shop-api's generated login with a bearer
// session, gives client the token and returns it.
func signIn(t *testing.T, client *sdk.ShopApiSDK, login, password string) string {
	t.Helper()
	result, err := client.AccountNamespace.Login(context.Background(), types.LoginInput{
		Login:    types.ContactEmail(login),
		Password: types.AuthPassword(password),
	})
	if err != nil {
		t.Fatalf("login as %s: %v", login, err)
	}
	if result.Token == "" {
		t.Fatalf("a bearer login answered no token: %+v", result)
	}
	client.SetToken(result.Token)
	return result.Token
}

// The generated Go SDK calls the generated Go server, which calls Products
// for a caller signed in through the SDK's login.
func TestTheSDKCallsTheGeneratedServer(t *testing.T) {
	u := newUsers(t)
	u.add(t, staffLogin, "Ada Lovelace", staffPassword, "products")
	server := productsServer(t, u)
	ctx := context.Background()

	client := productsClient(t, server)
	signIn(t, client, staffLogin, staffPassword)

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

	_, err = productsClient(t, server).ProductNamespace.ListProducts(ctx, nil)
	var unauthenticated *sdk.AuthenticationError
	if !errors.As(err, &unauthenticated) {
		t.Fatalf("ListProducts without a session: %v", err)
	}
}

// A user signs in and out through shop-api's session routes, which the
// identity runtime serves: me answers who they are and capabilities which
// routes admit them, and once they sign out the session authenticates no
// one.
func TestSignInAndOut(t *testing.T) {
	u := newUsers(t)
	u.add(t, staffLogin, "Ada Lovelace", staffPassword, "products")
	server := productsServer(t, u)
	ctx := context.Background()
	client := productsClient(t, server)

	_, err := client.AccountNamespace.Login(ctx, types.LoginInput{Login: staffLogin, Password: "not her password"})
	var unauthenticated *sdk.AuthenticationError
	if !errors.As(err, &unauthenticated) || unauthenticated.Code != "invalid_credentials" {
		t.Fatalf("login with a wrong password: %v", err)
	}
	// The login is a Contact.Email, which finds the user in any case.
	signIn(t, client, "Ada@Example.com", staffPassword)

	me, err := client.AccountNamespace.Me(ctx)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me.User.Login != staffLogin || me.User.Name != "Ada Lovelace" || len(me.Roles) != 1 || strings.Join(me.Permissions, ",") != "products" {
		t.Fatalf("Me returned %+v", me)
	}

	capabilities, err := client.AccountNamespace.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	for operation, want := range map[string]bool{
		"ProductCreateProductHandler":   true,
		"ProductListProductsHandler":    true,
		"AccountMeHandler":              true,
		"AccountAdminListUsersHandler":  false,
		"AccountAdminCreateRoleHandler": false,
	} {
		if got, listed := capabilities.Operations[operation]; !listed || got != want {
			t.Errorf("capabilities[%s] = %v (listed %v), want %v", operation, got, listed, want)
		}
	}

	if _, err := client.ProductNamespace.ListProducts(ctx, nil); err != nil {
		t.Fatalf("ListProducts signed in: %v", err)
	}
	if out, err := client.AccountNamespace.Logout(ctx); err != nil || !out {
		t.Fatalf("Logout: %v, %v", out, err)
	}
	_, err = client.ProductNamespace.ListProducts(ctx, nil)
	if !errors.As(err, &unauthenticated) {
		t.Fatalf("ListProducts after logout: %v", err)
	}
	_, err = client.AccountNamespace.Me(ctx)
	if !errors.As(err, &unauthenticated) {
		t.Fatalf("Me after logout: %v", err)
	}
}

// A browser signs in with a cookie session: the login sets the session
// cookie and answers no token, and the cookie authenticates the requests
// that follow. A cookie login from another site, and a cookie request from
// another site that changes state, are refused 403 cross_origin, unless the
// identity config trusts the site.
func TestCookieSignIn(t *testing.T) {
	u := newUsers(t)
	u.add(t, staffLogin, "Ada Lovelace", staffPassword, "products")
	server := productsServer(t, u)
	send := func(method, path, body string, headers ...string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(headers); i += 2 {
			request.Header.Set(headers[i], headers[i+1])
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response
	}
	credentials := `{"login": "` + staffLogin + `", "password": "` + staffPassword + `", "session": "cookie"}`

	refused := send(http.MethodPost, "/api/auth/login", credentials, "Origin", "https://elsewhere.example.com", "Sec-Fetch-Site", "cross-site")
	if refused.StatusCode != http.StatusForbidden || len(refused.Cookies()) != 0 {
		t.Fatalf("a cookie login from another site answered %d with %v", refused.StatusCode, refused.Cookies())
	}

	signedIn := send(http.MethodPost, "/api/auth/login", credentials, "Sec-Fetch-Site", "same-origin")
	cookies := signedIn.Cookies()
	if signedIn.StatusCode != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("a same-origin cookie login answered %d with %v", signedIn.StatusCode, cookies)
	}
	// The cookie no script reads: __Host-session, HttpOnly, Secure and
	// SameSite=Lax by default. The local target's servers, on plain HTTP,
	// write session without Secure.
	cookie := cookies[0]
	if cookie.Name != "__Host-session" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("the session cookie is %s", cookie)
	}
	withCookie := cookie.Name + "=" + cookie.Value

	if me := send(http.MethodGet, "/api/auth/me", "", "Cookie", withCookie); me.StatusCode != http.StatusOK {
		t.Fatalf("me with the cookie answered %d", me.StatusCode)
	}
	product := `{"sku": "tea", "name": "Tea", "priceCents": 300}`
	if created := send(http.MethodPost, "/api/products", product, "Cookie", withCookie, "Origin", "https://elsewhere.example.com", "Sec-Fetch-Site", "cross-site"); created.StatusCode != http.StatusForbidden {
		t.Fatalf("a cookie request from another site answered %d", created.StatusCode)
	}
	trusted := send(http.MethodPost, "/api/products", product, "Cookie", withCookie, "Origin", storefrontSite, "Sec-Fetch-Site", "cross-site")
	if trusted.StatusCode != http.StatusOK || trusted.Header.Get("Access-Control-Allow-Origin") != storefrontSite {
		t.Fatalf("a cookie request from the trusted storefront answered %d with %v", trusted.StatusCode, trusted.Header)
	}
}

// The server checks what it receives as the SDK checks what it sends, and
// serves its OpenAPI document without a caller.
func TestTheServerValidatesAndDescribesItself(t *testing.T) {
	u := newUsers(t)
	u.add(t, staffLogin, "Ada Lovelace", staffPassword, "products")
	server := productsServer(t, u)
	token := signIn(t, productsClient(t, server), staffLogin, staffPassword)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/products",
		strings.NewReader(`{"sku": "tea", "name": "Tea", "priceCents": -1}`))
	request.Header.Set("Authorization", "Bearer "+token)
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
