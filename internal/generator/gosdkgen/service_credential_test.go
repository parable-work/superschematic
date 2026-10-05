package gosdkgen

import "testing"

// TestServiceCredentialAndForwarding runs serviceCredentialSDKTest in the
// generated SDK (D37): every request carries the service credential in each
// configured header; a 401 with the code service_unauthorized, in an RFC
// 9457 problem or the legacy {"error": {...}} envelope, asks the source for a fresh token once
// and never runs the end-user refresh; any other 401 refreshes the end user
// and never asks for a fresh service token; a call spends at most one of
// each. A server forwards its end user through AuthConfig.GetToken, which
// reads the token of the request on the call's context: with no static
// token it is asked on every call, and nothing it returns is kept.
func TestServiceCredentialAndForwarding(t *testing.T) {
	runInNestedArraysSDK(t, "service_credential_test.go", serviceCredentialSDKTest)
}

// serviceCredentialSDKTest runs in the generated SDK module against an
// httptest server that answers each request with the next canned response.
const serviceCredentialSDKTest = `package sdk_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-nested-arrays-api"
)

const gridID = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"

const (
	ok                 = "{\"data\":[],\"meta\":{\"requestId\":\"req-1\"}}"
	serviceRefusal     = "{\"title\":\"Unauthorized\",\"status\":401,\"detail\":\"Invalid service credential\",\"code\":\"service_unauthorized\"}"
	userRefusal        = "{\"title\":\"Unauthorized\",\"status\":401,\"detail\":\"Authentication required\",\"code\":\"unauthorized\"}"
	legacyServiceRefusal = "{\"error\":{\"code\":\"service_unauthorized\",\"message\":\"Invalid service credential\"}}"
)

type reply struct {
	status int
	body   string
}

// server answers each request with the next reply and records its headers.
type server struct {
	mu      sync.Mutex
	replies []reply
	headers []http.Header
}

func serve(t *testing.T, replies ...reply) (*server, string) {
	t.Helper()
	s := &server{replies: replies}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.headers = append(s.headers, r.Header.Clone())
		if len(s.headers) > len(s.replies) {
			t.Errorf("unexpected request %d", len(s.headers))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		next := s.replies[len(s.headers)-1]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.status)
		_, _ = io.WriteString(w, next.body)
	}))
	t.Cleanup(ts.Close)
	return s, ts.URL
}

func (s *server) header(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]string, len(s.headers))
	for i, h := range s.headers {
		values[i] = h.Get(name)
	}
	return values
}

// source is a service credential source that records each fresh flag and
// returns service-1, service-2, ...
type source struct {
	mu    sync.Mutex
	fresh []bool
}

func (s *source) token(_ context.Context, fresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fresh = append(s.fresh, fresh)
	return "service-" + string(rune('0'+len(s.fresh))), nil
}

type refresher struct{ calls int }

func (r *refresher) refresh(context.Context) (string, error) {
	r.calls++
	return "user-refreshed", nil
}

func newClient(t *testing.T, config sdk.SDKConfig) *sdk.FixtureNestedArraysApiSDK {
	t.Helper()
	client, err := sdk.New(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func call(ctx context.Context, client *sdk.FixtureNestedArraysApiSDK) error {
	_, err := client.GridNamespace.GridLabels(ctx, gridID, nil)
	return err
}

func requireAuthenticationError(t *testing.T, err error, code string) {
	t.Helper()
	var authErr *sdk.AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %T %v, want *sdk.AuthenticationError", err, err)
	}
	if authErr.Code != code {
		t.Errorf("Code = %q, want %q", authErr.Code, code)
	}
}

func equal[T any](t *testing.T, name string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestTheServiceCredentialIsSentOnEveryRequest(t *testing.T) {
	s, url := serve(t, reply{200, ok}, reply{200, ok})
	src := &source{}
	client := newClient(t, sdk.SDKConfig{
		BaseURL:           url,
		Auth:              &sdk.AuthConfig{Token: "alice"},
		ServiceCredential: &sdk.ServiceCredentialConfig{Token: src.token},
	})
	for range 2 {
		if err := call(context.Background(), client); err != nil {
			t.Fatal(err)
		}
	}
	equal(t, "Service-Authorization", s.header("Service-Authorization"), []string{"Bearer service-1", "Bearer service-2"})
	equal(t, "Authorization", s.header("Authorization"), []string{"Bearer alice", "Bearer alice"})
	equal(t, "fresh", src.fresh, []bool{false, false})
}

func TestTheServiceCredentialIsSentInEachConfiguredHeader(t *testing.T) {
	s, url := serve(t, reply{200, ok})
	src := &source{}
	client := newClient(t, sdk.SDKConfig{
		BaseURL: url,
		ServiceCredential: &sdk.ServiceCredentialConfig{
			Token:   src.token,
			Headers: []string{"Service-Authorization", "X-Serverless-Authorization"},
		},
	})
	if err := call(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	equal(t, "Service-Authorization", s.header("Service-Authorization"), []string{"Bearer service-1"})
	equal(t, "X-Serverless-Authorization", s.header("X-Serverless-Authorization"), []string{"Bearer service-1"})
	equal(t, "Authorization", s.header("Authorization"), []string{""})
}

func TestAServiceRefusalAsksForAFreshTokenOnce(t *testing.T) {
	for _, body := range []string{serviceRefusal, legacyServiceRefusal} {
		t.Run(body, func(t *testing.T) {
			s, url := serve(t, reply{401, body}, reply{200, ok})
			src, user := &source{}, &refresher{}
			client := newClient(t, sdk.SDKConfig{
				BaseURL:           url,
				Auth:              &sdk.AuthConfig{Token: "alice", RefreshToken: user.refresh},
				ServiceCredential: &sdk.ServiceCredentialConfig{Token: src.token},
			})
			if err := call(context.Background(), client); err != nil {
				t.Fatal(err)
			}
			equal(t, "fresh", src.fresh, []bool{false, true})
			equal(t, "end-user refreshes", user.calls, 0)
			equal(t, "Service-Authorization", s.header("Service-Authorization"), []string{"Bearer service-1", "Bearer service-2"})
			equal(t, "Authorization", s.header("Authorization"), []string{"Bearer alice", "Bearer alice"})
		})
	}
}

func TestASecondServiceRefusalIsTheCallersError(t *testing.T) {
	s, url := serve(t, reply{401, serviceRefusal}, reply{401, serviceRefusal})
	src, user := &source{}, &refresher{}
	client := newClient(t, sdk.SDKConfig{
		BaseURL:           url,
		Auth:              &sdk.AuthConfig{Token: "alice", RefreshToken: user.refresh},
		ServiceCredential: &sdk.ServiceCredentialConfig{Token: src.token},
	})
	requireAuthenticationError(t, call(context.Background(), client), "service_unauthorized")
	equal(t, "requests", len(s.header("Service-Authorization")), 2)
	equal(t, "fresh", src.fresh, []bool{false, true})
	equal(t, "end-user refreshes", user.calls, 0)
}

func TestAnEndUserRefusalNeverAsksTheServiceSource(t *testing.T) {
	for _, body := range []string{userRefusal, "{\"title\":\"Unauthorized\"}"} {
		t.Run(body, func(t *testing.T) {
			s, url := serve(t, reply{401, body}, reply{200, ok})
			src, user := &source{}, &refresher{}
			client := newClient(t, sdk.SDKConfig{
				BaseURL:           url,
				Auth:              &sdk.AuthConfig{Token: "alice", RefreshToken: user.refresh},
				ServiceCredential: &sdk.ServiceCredentialConfig{Token: src.token},
			})
			if err := call(context.Background(), client); err != nil {
				t.Fatal(err)
			}
			equal(t, "end-user refreshes", user.calls, 1)
			equal(t, "fresh", src.fresh, []bool{false, false})
			equal(t, "Authorization", s.header("Authorization"), []string{"Bearer alice", "Bearer user-refreshed"})
		})
	}
}

func TestOneCallRetriesEachCredentialOnce(t *testing.T) {
	s, url := serve(t, reply{401, serviceRefusal}, reply{401, userRefusal}, reply{401, serviceRefusal})
	src, user := &source{}, &refresher{}
	client := newClient(t, sdk.SDKConfig{
		BaseURL:           url,
		Auth:              &sdk.AuthConfig{Token: "alice", RefreshToken: user.refresh},
		ServiceCredential: &sdk.ServiceCredentialConfig{Token: src.token},
	})
	requireAuthenticationError(t, call(context.Background(), client), "service_unauthorized")
	equal(t, "requests", len(s.header("Authorization")), 3)
	equal(t, "fresh", src.fresh, []bool{false, true, false})
	equal(t, "end-user refreshes", user.calls, 1)
}

func TestWithoutAServiceCredentialAServiceRefusalDoesNotRefreshTheUser(t *testing.T) {
	s, url := serve(t, reply{401, serviceRefusal})
	user := &refresher{}
	client := newClient(t, sdk.SDKConfig{
		BaseURL: url,
		Auth:    &sdk.AuthConfig{Token: "alice", RefreshToken: user.refresh},
	})
	requireAuthenticationError(t, call(context.Background(), client), "service_unauthorized")
	equal(t, "requests", len(s.header("Authorization")), 1)
	equal(t, "end-user refreshes", user.calls, 0)
}

type userKey struct{}

// forwardedToken stands in for serviceauth.ForwardedToken: the bearer token
// of the request being served, or "" with no error when it has none.
func forwardedToken(ctx context.Context) (string, error) {
	token, _ := ctx.Value(userKey{}).(string)
	return token, nil
}

// An edge client forwards the end user of the request on the call's
// context, through GetToken, and holds no token or refresh of its own.
func TestGetTokenForwardsTheEndUserOnTheCallsContext(t *testing.T) {
	s, url := serve(t, reply{200, ok}, reply{200, ok}, reply{200, ok}, reply{401, userRefusal})
	src := &source{}
	client := newClient(t, sdk.SDKConfig{
		BaseURL:           url,
		Auth:              &sdk.AuthConfig{GetToken: forwardedToken},
		ServiceCredential: &sdk.ServiceCredentialConfig{Token: src.token},
	})
	for _, user := range []string{"alice", "bob", ""} {
		if err := call(context.WithValue(context.Background(), userKey{}, user), client); err != nil {
			t.Fatal(err)
		}
	}
	// No refresh is configured, so an end-user 401 is the caller's error.
	requireAuthenticationError(t, call(context.WithValue(context.Background(), userKey{}, "carol"), client), "unauthorized")
	equal(t, "Authorization", s.header("Authorization"), []string{"Bearer alice", "Bearer bob", "", "Bearer carol"})
	equal(t, "Service-Authorization", s.header("Service-Authorization"), []string{"Bearer service-1", "Bearer service-2", "Bearer service-3", "Bearer service-4"})
}
`
