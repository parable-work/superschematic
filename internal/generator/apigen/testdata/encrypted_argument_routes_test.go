// This file runs inside the generated API module of encrypted-argument-api
// (TestEncryptedArgumentRoutesDecrypt copies it there). storeCard takes an
// EncryptedField<string> argument outside an Encrypted operation set, so its
// route decrypts the request body before it decodes any argument;
// renameCard takes none and stays a plain JSON route.
package encryptedargumentapi_test

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
	"go.uber.org/zap"

	api "example.com/schemas/api/encrypted-argument-api"
	types "example.com/schemas/types/go/encrypted-argument-api"
)

// base64Decryptor stands in for a key service: the ciphertext is the base64
// of the plaintext, and every call is counted.
type base64Decryptor struct {
	mu    sync.Mutex
	calls int
}

func (d *base64Decryptor) Decrypt(_ context.Context, ciphertext string) ([]byte, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return base64.StdEncoding.DecodeString(ciphertext)
}

func (d *base64Decryptor) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// cards records the arguments of the last call.
type cards struct {
	mu    sync.Mutex
	calls int
	last  []string
}

func (c *cards) record(args ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.last = args
}

func (c *cards) StoreCard(_ context.Context, customerID string, number string, label string) (*types.CardReceipt, error) {
	c.record(customerID, number, label)
	return &types.CardReceipt{CustomerId: customerID, Label: label}, nil
}

func (c *cards) RenameCard(_ context.Context, id string, label string) (*types.CardReceipt, error) {
	c.record(id, label)
	return &types.CardReceipt{Label: label}, nil
}

func (c *cards) snapshot() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, strings.Join(c.last, ",")
}

func serve(t *testing.T) (*httptest.Server, *cards, *base64Decryptor) {
	t.Helper()
	impl := &cards{}
	decryptor := &base64Decryptor{}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:           zap.NewNop(),
		PayloadDecryptor: decryptor,
		Implementations:  api.Implementations{Card: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl, decryptor
}

// envelope wraps a JSON body in the encrypted request envelope the
// base64Decryptor opens.
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

func send(t *testing.T, server *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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

const cardBody = `{"number": "4242424242424242", "label": "work"}`

// TestAnEncryptedArgumentRefusesAPlainBody: storeCard answers 400 to its
// arguments sent as plain JSON, without calling the implementation.
func TestAnEncryptedArgumentRefusesAPlainBody(t *testing.T) {
	server, impl, _ := serve(t)
	status, response := send(t, server, http.MethodPost, "/api/customers/c-1/cards", cardBody)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a plain body reached an encrypted route; response %s", status, response)
	}
	if calls, _ := impl.snapshot(); calls != 0 {
		t.Fatal("a plain body reached the implementation")
	}
}

// TestAnEncryptedArgumentIsDecrypted: storeCard decrypts the envelope and
// decodes every body argument from the plaintext; the path parameter comes
// from the path as before.
func TestAnEncryptedArgumentIsDecrypted(t *testing.T) {
	server, impl, decryptor := serve(t)
	status, response := send(t, server, http.MethodPost, "/api/customers/c-1/cards", envelope(t, cardBody))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; response %s", status, response)
	}
	if got := decryptor.count(); got != 1 {
		t.Fatalf("the decryptor ran %d time(s), want 1", got)
	}
	if calls, last := impl.snapshot(); calls != 1 || last != "c-1,4242424242424242,work" {
		t.Fatalf("implementation calls = %d, last = %q", calls, last)
	}
}

// TestAnOperationWithoutOneStaysPlain: renameCard, in the same set, takes
// plain JSON and never reaches the decryptor.
func TestAnOperationWithoutOneStaysPlain(t *testing.T) {
	server, impl, decryptor := serve(t)
	status, response := send(t, server, http.MethodPatch, "/api/cards/card-1", `{"label": "home"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; response %s", status, response)
	}
	if got := decryptor.count(); got != 0 {
		t.Fatalf("the decryptor ran %d time(s) for a plain route", got)
	}
	if calls, last := impl.snapshot(); calls != 1 || last != "card-1,home" {
		t.Fatalf("implementation calls = %d, last = %q", calls, last)
	}
}
