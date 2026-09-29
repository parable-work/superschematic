package gosdkgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

// encryptedArgumentService is apigen's TypeScript schema with an
// EncryptedField<string> argument outside an Encrypted operation set.
const encryptedArgumentService = "encrypted-argument-api"

// TestEncryptedArgumentSDKBuildsAndRuns runs encryptedArgumentSDKTest in
// the generated SDK: storeCard, which takes an EncryptedField<string>
// argument, takes EncryptedRequestOptions and sends its body as an RSA-OAEP
// envelope the private key opens to that body, with the card number
// nowhere in the clear; renameCard sends plain JSON.
func TestEncryptedArgumentSDKBuildsAndRuns(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", encryptedArgumentService))
	if err != nil {
		t.Fatalf("load %s: %v", encryptedArgumentService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  encryptedArgumentService,
		ModulePath:  "example.com/schemas/api/" + encryptedArgumentService,
		TypesModule: "example.com/schemas/types/go/" + encryptedArgumentService,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	runInSDK(t, schema, apiOutput, encryptedArgumentService, "encrypted_argument_test.go", encryptedArgumentSDKTest)
}

// encryptedArgumentSDKTest runs in the generated SDK module against an
// httptest server that records each request body and answers a
// CardReceipt in the success envelope.
const encryptedArgumentSDKTest = `package sdk_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "example.com/schemas/sdk/go/encrypted-argument-api"
	"example.com/schemas/sdk/go/encrypted-argument-api/namespaces"
)

type call struct {
	method, path, body string
}

func serve(t *testing.T, publicKey string) (*sdk.EncryptedArgumentApiSDK, *[]call) {
	t.Helper()
	calls := &[]call{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*calls = append(*calls, call{r.Method, r.URL.Path, string(raw)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ` + "`" + `{"data":{"customerId":"c-1","label":"work"},"meta":{"requestId":"req-1"}}` + "`" + `)
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{
		BaseURL: server.URL,
		Encryption: &sdk.EncryptionConfig{PublicEncryptionKey: &sdk.PublicEncryptionKey{
			PublicKey: publicKey, Algorithm: "RSA_OAEP_256", KeyID: "key-1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, calls
}

func newKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func TestStoreCardSendsAnEncryptedEnvelope(t *testing.T) {
	key, publicKey := newKey(t)
	client, calls := serve(t, publicKey)
	label := "work"
	if _, err := client.CardNamespace.StoreCard(context.Background(), "c-1", namespaces.CardStoreCardInput{
		Number: "4242424242424242",
		Label:  &label,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].method != "POST" || (*calls)[0].path != "/api/customers/c-1/cards" {
		t.Fatalf("calls = %+v", *calls)
	}
	body := (*calls)[0].body
	if strings.Contains(body, "4242424242424242") {
		t.Fatalf("the card number travels in the clear: %s", body)
	}
	var envelope struct {
		Algorithm string ` + "`" + `json:"algorithm"` + "`" + `
		Payload   string ` + "`" + `json:"payload"` + "`" + `
		KeyID     string ` + "`" + `json:"keyId"` + "`" + `
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("body is not an envelope: %s", body)
	}
	if envelope.Algorithm != "RSA_OAEP_256" || envelope.KeyID != "key-1" {
		t.Fatalf("envelope = %+v", envelope)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, ciphertext, nil)
	if err != nil {
		t.Fatalf("the private key does not open the payload: %v", err)
	}
	// The plaintext is the request body itself, which the server decodes
	// as it would a plain JSON body.
	if string(plaintext) != ` + "`" + `{"number":"4242424242424242","label":"work"}` + "`" + ` {
		t.Errorf("plaintext = %s, want the body {number, label}", plaintext)
	}
}

func TestRenameCardSendsPlainJSON(t *testing.T) {
	_, publicKey := newKey(t)
	client, calls := serve(t, publicKey)
	if _, err := client.CardNamespace.RenameCard(context.Background(), "card-1", namespaces.CardRenameCardInput{Label: "home"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].method != "PATCH" || (*calls)[0].body != ` + "`" + `{"label":"home"}` + "`" + ` {
		t.Fatalf("calls = %+v", *calls)
	}
}
`
