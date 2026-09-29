// This file runs inside the generated API module of encrypted-api
// (TestEncryptedRoutesRefuseBeforeDecrypting copies it there). It registers
// the generated routes with a payload decryptor that counts its calls and
// checks that a request the permission check, the body limit or the rate
// limit refuses never reaches the decryptor, which in a deployment is a call
// to a key service.
package encryptedapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	runtimesession "github.com/parable-work/superschematic/runtime/http/go/session"
	"go.uber.org/zap"

	api "example.com/schemas/api/encrypted-api"
	types "example.com/schemas/types/go/encrypted-api"
)

// countingDecryptor stands in for a key service: the ciphertext is the
// base64 of the plaintext, and every call is counted.
type countingDecryptor struct {
	mu    sync.Mutex
	calls int
}

func (d *countingDecryptor) Decrypt(_ context.Context, ciphertext string) ([]byte, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return base64.StdEncoding.DecodeString(ciphertext)
}

func (d *countingDecryptor) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// vault records the arguments of the last call.
type vault struct {
	mu    sync.Mutex
	calls int
	last  []string
}

func (v *vault) record(name string, args ...string) *types.SecretReceipt {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	v.last = append([]string{name}, args...)
	return &types.SecretReceipt{Name: name}
}

func (v *vault) StoreSecret(_ context.Context, name string, value string) (*types.SecretReceipt, error) {
	return v.record(name, value), nil
}

func (v *vault) RotateSecret(_ context.Context, name string, reason string) (*types.SecretReceipt, error) {
	return v.record(name, reason), nil
}

func (v *vault) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

// permissionsHeader carries the caller's granted permissions, comma
// separated. Without it the request has no caller.
const permissionsHeader = "X-Test-Permissions"

// caller puts a principal holding the header's permissions on the context,
// as a service's own auth middleware does.
func caller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		granted := r.Header.Get(permissionsHeader)
		if granted == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := runtimesession.ContextWithPrincipalID(r.Context(), "caller-1")
		ctx = runtimesession.ContextWithRoles(ctx, []runtimesession.Role{{
			ID:          "role-1",
			Name:        "test",
			Permissions: strings.Split(granted, ","),
		}})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func serve(t *testing.T) (*httptest.Server, *vault, *countingDecryptor) {
	t.Helper()
	impl := &vault{}
	decryptor := &countingDecryptor{}
	router := chi.NewRouter()
	router.Use(caller)
	if err := api.RegisterRoutes(router, api.Config{
		Logger:           zap.NewNop(),
		PayloadDecryptor: decryptor,
		Implementations:  api.Implementations{Vault: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl, decryptor
}

// envelope wraps a JSON body in the encrypted request envelope the
// countingDecryptor opens.
func envelope(t *testing.T, plaintext string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"algorithm": "RSA_OAEP_256",
		"payload":   base64.StdEncoding.EncodeToString([]byte(plaintext)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// send posts body as the caller holding permissions ("" for no caller) and
// returns the status and the response body.
func send(t *testing.T, server *httptest.Server, path, permissions, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if permissions != "" {
		req.Header.Set(permissionsHeader, permissions)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

const (
	storeSecretPath  = "/api/secrets"
	rotateSecretPath = "/api/secrets/db-password/rotate"
)

// TestThePermissionCheckRunsBeforeTheDecryptor: a request without a caller
// answers 401 and one whose caller lacks the permission answers 403, and
// neither reaches the decryptor or the implementation. A caller holding the
// permission gets the decrypted arguments through.
func TestThePermissionCheckRunsBeforeTheDecryptor(t *testing.T) {
	server, impl, decryptor := serve(t)
	body := envelope(t, `{"name": "db-password", "value": "hunter2"}`)

	for _, tc := range []struct {
		name, permissions string
		want              int
	}{
		{name: "no caller", permissions: "", want: http.StatusUnauthorized},
		{name: "caller without the permission", permissions: "secrets.read", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, response := send(t, server, storeSecretPath, tc.permissions, body)
			if status != tc.want {
				t.Fatalf("status = %d, want %d; response %s", status, tc.want, response)
			}
			if got := decryptor.count(); got != 0 {
				t.Fatalf("the decryptor ran %d time(s) for a request the permission check refuses", got)
			}
			if impl.count() != 0 {
				t.Fatal("a refused request reached the implementation")
			}
		})
	}

	status, response := send(t, server, storeSecretPath, "secrets.write", body)
	if status != http.StatusOK {
		t.Fatalf("permitted caller: status = %d, want 200; response %s", status, response)
	}
	if got := decryptor.count(); got != 1 {
		t.Fatalf("permitted caller: the decryptor ran %d time(s), want 1", got)
	}
	if impl.count() != 1 || strings.Join(impl.last, ",") != "db-password,hunter2" {
		t.Fatalf("permitted caller: implementation calls = %d, last = %v", impl.count(), impl.last)
	}
}

// TestTheBodyLimitRunsBeforeTheDecryptor: an envelope larger than the
// route's @bodyLimit answers 413 without reaching the decryptor, even from
// a caller holding the permission.
func TestTheBodyLimitRunsBeforeTheDecryptor(t *testing.T) {
	server, impl, decryptor := serve(t)
	large := `{"name": "db-password", "value": "` + strings.Repeat("x", 1<<20) + `"}`
	status, response := send(t, server, storeSecretPath, "secrets.write", envelope(t, large))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; response %.200s", status, response)
	}
	if got := decryptor.count(); got != 0 {
		t.Fatalf("the decryptor ran %d time(s) for a body over the limit", got)
	}
	if impl.count() != 0 {
		t.Fatal("a refused request reached the implementation")
	}
}

// TestTheRateLimitRunsBeforeTheDecryptor: past the route's @rateLimit a
// request answers 429 without reaching the decryptor.
func TestTheRateLimitRunsBeforeTheDecryptor(t *testing.T) {
	server, impl, decryptor := serve(t)
	body := envelope(t, `{"reason": "scheduled"}`)

	status, response := send(t, server, rotateSecretPath, "secrets.write", body)
	if status != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200; response %s", status, response)
	}
	status, response = send(t, server, rotateSecretPath, "secrets.write", body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("second request: status = %d, want 429; response %s", status, response)
	}
	if got := decryptor.count(); got != 1 {
		t.Fatalf("the decryptor ran %d time(s), want 1: a rate-limited request reached it", got)
	}
	if impl.count() != 1 {
		t.Fatalf("implementation calls = %d, want 1", impl.count())
	}
}
