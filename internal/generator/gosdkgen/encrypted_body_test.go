package gosdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// encryptedBodyService is apigen's TypeScript schema with an Encrypted
// operation set: openWallet takes an input type, resetPin a path parameter
// and scalar arguments.
const encryptedBodyService = "encrypted-body-api"

// TestEncryptedBodiesReachTheGoServer generates the Go types module, the Go
// API module and the Go SDK of encrypted-body-api into one temp tree, then
// runs encryptedBodyServerTest in the API module: the SDK encrypts each
// body, the generated routes open it with the runtime's payload decryptor
// middleware and a key the test generates, and the implementation receives
// the arguments the SDK was given. The decrypted plaintext is the request
// body itself, as the TypeScript and Python SDKs send it; a wrapper around
// it would reach the handler without its arguments.
func TestEncryptedBodiesReachTheGoServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", encryptedBodyService))
	if err != nil {
		t.Fatalf("load %s: %v", encryptedBodyService, err)
	}
	paths := testpaths.Local(t)
	typesModule := "example.com/schemas/types/go/" + encryptedBodyService
	sdkModule := "example.com/schemas/sdk/go/" + encryptedBodyService

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", encryptedBodyService)
	apiDir := filepath.Join(root, "api", encryptedBodyService)
	sdkDir := filepath.Join(root, "sdk", "go", encryptedBodyService)

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: encryptedBodyService,
		ModulePath: typesModule,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}

	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  encryptedBodyService,
		ModulePath:  "example.com/schemas/api/" + encryptedBodyService,
		TypesModule: typesModule,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if err := apigen.SetReplacePaths(apiOutput, paths, apiDir); err != nil {
		t.Fatalf("apigen.SetReplacePaths: %v", err)
	}
	if err := apigen.WriteAPI(apiOutput, apiDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}

	sdkOutput, err := Generate(apiOutput, sdkModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	// The API module already replaces every module the SDK needs; it
	// only needs the SDK itself.
	if err := os.WriteFile(filepath.Join(apiDir, "encrypted_body_test.go"), []byte(encryptedBodyServerTest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"mod", "edit", "-replace", sdkModule + "=../../sdk/go/" + encryptedBodyService},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
		// No cmd.Env: exec then sets PWD to cmd.Dir, which keeps the
		// module's relative replace paths valid under a symlinked temp dir.
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[0] == "test" {
			t.Logf("generated API tests:\n%s", out)
		}
	}
}

// encryptedBodyServerTest runs in the generated API module of
// encrypted-body-api, with the Go SDK replaced in.
const encryptedBodyServerTest = `package encryptedbodyapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/encrypted-body-api"
	sdk "example.com/schemas/sdk/go/encrypted-body-api"
	"example.com/schemas/sdk/go/encrypted-body-api/namespaces"
	types "example.com/schemas/types/go/encrypted-body-api"
)

// keyService stands in for a key service: it opens RSA-OAEP ciphertext with
// the private key whose public half the SDK encrypts to. The runtime's
// middleware calls it for the payload of an RSA_OAEP_256 envelope and for
// the wrapped AES key of a hybrid one.
type keyService struct {
	key *rsa.PrivateKey
}

func (k keyService) Decrypt(_ context.Context, ciphertext string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	return rsa.DecryptOAEP(sha256.New(), nil, k.key, raw, nil)
}

// wallets records the arguments of the last call.
type wallets struct {
	mu   sync.Mutex
	last []string
}

func (w *wallets) record(args ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last = args
}

func (w *wallets) lastCall() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

func (w *wallets) OpenWallet(_ context.Context, input *types.OpenWalletInput) (*types.WalletReceipt, error) {
	w.record("openWallet", input.Owner, input.Pin)
	return &types.WalletReceipt{Owner: input.Owner}, nil
}

func (w *wallets) ResetPin(_ context.Context, id string, pin string, hint string) (*types.WalletReceipt, error) {
	w.record("resetPin", id, pin, hint)
	return &types.WalletReceipt{Owner: id, Hint: hint}, nil
}

// serve mounts the generated routes with keyService as the payload
// decryptor and returns an SDK that encrypts to its public key with
// algorithm.
func serve(t *testing.T, algorithm string) (*sdk.EncryptedBodyApiSDK, *wallets) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	impl := &wallets{}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:           zap.NewNop(),
		PayloadDecryptor: keyService{key: key},
		Implementations:  api.Implementations{Wallet: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{
		BaseURL: server.URL,
		Encryption: &sdk.EncryptionConfig{PublicEncryptionKey: &sdk.PublicEncryptionKey{
			PublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
			Algorithm: algorithm,
			KeyID:     "key-1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, impl
}

func TestEncryptedBodiesReachTheImplementation(t *testing.T) {
	for _, algorithm := range []string{"RSA_OAEP_256", "AES_256_GCM_RSA_OAEP_256"} {
		t.Run(algorithm, func(t *testing.T) {
			client, impl := serve(t, algorithm)
			ctx := context.Background()

			opened, err := client.WalletNamespace.OpenWallet(ctx, types.OpenWalletInput{Owner: "ada", Pin: "1234"}, nil)
			if err != nil {
				t.Fatalf("OpenWallet: %v", err)
			}
			if got, want := impl.lastCall(), []string{"openWallet", "ada", "1234"}; !slices.Equal(got, want) {
				t.Errorf("openWallet decoded %q, want %q", got, want)
			}
			if opened.Owner != "ada" {
				t.Errorf("OpenWallet returned %+v", opened)
			}

			hint := "birthday"
			reset, err := client.WalletNamespace.ResetPin(ctx, "w-1", namespaces.WalletResetPinInput{Pin: "5678", Hint: &hint}, nil)
			if err != nil {
				t.Fatalf("ResetPin: %v", err)
			}
			if got, want := impl.lastCall(), []string{"resetPin", "w-1", "5678", "birthday"}; !slices.Equal(got, want) {
				t.Errorf("resetPin decoded %q, want %q", got, want)
			}
			if reset.Owner != "w-1" || reset.Hint != "birthday" {
				t.Errorf("ResetPin returned %+v", reset)
			}
		})
	}
}
`
