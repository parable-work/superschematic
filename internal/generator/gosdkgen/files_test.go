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

const filesService = "fixture-files-api"

// TestFileTransfersThroughTheGoSDK generates the Go types module, the Go
// API module and the Go SDK of fixture-files-api into one temp tree, then
// runs filesServerTest in the API module: the SDK sends a statement as a
// multipart body the generated routes read, its refusals are the SDK's
// validation errors at the file's field, and each FileDownload result
// (D81) comes back as a DownloadedFile with its bytes, filename and media
// type.
func TestFileTransfersThroughTheGoSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join("..", "..", "loader", "tsreader", "testdata", "services", filesService))
	if err != nil {
		t.Fatalf("load %s: %v", filesService, err)
	}
	paths := testpaths.Local(t)
	typesModule := "example.com/schemas/types/go/" + filesService
	sdkModule := "example.com/schemas/sdk/go/" + filesService

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", filesService)
	apiDir := filepath.Join(root, "api", filesService)
	sdkDir := filepath.Join(root, "sdk", "go", filesService)

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: filesService, ModulePath: typesModule, Clock: nestedArraysClock})
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
		SchemaName:  filesService,
		ModulePath:  "example.com/schemas/api/" + filesService,
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

	if err := os.WriteFile(filepath.Join(apiDir, "files_sdk_test.go"), []byte(filesServerTest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"mod", "edit", "-replace", sdkModule + "=../../sdk/go/" + filesService},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
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

// filesServerTest runs in the generated API module of fixture-files-api,
// with the Go SDK replaced in.
const filesServerTest = `package fixturefilesapi_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-files-api"
	sdk "example.com/schemas/sdk/go/fixture-files-api"
	"example.com/schemas/sdk/go/fixture-files-api/namespaces"
	"example.com/schemas/sdk/go/fixture-files-api/runtime"
	types "example.com/schemas/types/go/fixture-files-api"
)

type statements struct {
	lines map[types.IdentityUUID][]string
	names map[types.IdentityUUID]string
	last  map[types.IdentityUUID][]byte
}

func (s *statements) ImportStatement(_ context.Context, input *types.ImportStatementInput, files map[string]*api.FileUploadData) (*types.ImportResult, error) {
	data, err := files["statement"].ReadAllBytes()
	if err != nil {
		return nil, err
	}
	rows := strings.Split(strings.TrimSpace(string(data)), "\n")[1:]
	s.lines[input.AccountId] = append(s.lines[input.AccountId], rows...)
	s.names[input.AccountId], s.last[input.AccountId] = files["statement"].Filename, data
	return &types.ImportResult{AccountId: input.AccountId, Filename: files["statement"].Filename, Lines: float64(len(rows))}, nil
}

func (s *statements) ExportStatement(_ context.Context, accountID types.IdentityUUID) (*api.FileDownload, error) {
	return &api.FileDownload{Body: strings.NewReader("date,amount\n" + strings.Join(s.lines[accountID], "\n") + "\n")}, nil
}

func (s *statements) OriginalStatement(_ context.Context, accountID types.IdentityUUID) (*api.FileDownload, error) {
	return &api.FileDownload{Body: bytes.NewReader(s.last[accountID]), Filename: s.names[accountID]}, nil
}

func TestFilesThroughTheSDK(t *testing.T) {
	impl := &statements{lines: map[types.IdentityUUID][]string{}, names: map[types.IdentityUUID]string{}, last: map[types.IdentityUUID][]byte{}}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{Logger: zap.NewNop(), Implementations: api.Implementations{Statements: impl}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	account, err := types.ParseIdentityUUID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}

	statement := runtime.UploadFile{Filename: "M\u00e4rz.csv", ContentType: "text/csv", Reader: []byte("date,amount\n2026-03-01,-12.50\n")}
	result, err := client.StatementsNamespace.ImportStatement(ctx, types.ImportStatementInput{AccountId: account}, namespaces.StatementsImportStatementFiles{Statement: statement})
	if err != nil {
		var refused *sdk.APIError
		if errors.As(err, &refused) {
			t.Fatalf("ImportStatement: %v: %s", err, refused.Body)
		}
		t.Fatalf("ImportStatement: %v", err)
	}
	if result.Filename != "M\u00e4rz.csv" || result.Lines != 1 {
		t.Errorf("ImportStatement = %+v", result)
	}

	exported, err := client.StatementsNamespace.ExportStatement(ctx, "1VgEh72lXvTXkH")
	if err != nil {
		t.Fatalf("ExportStatement: %v", err)
	}
	if exported.Filename != "statement.csv" || exported.ContentType != "text/csv; charset=utf-8" || string(exported.Body) != "date,amount\n2026-03-01,-12.50\n" {
		t.Errorf("ExportStatement = %+v (%q)", exported, exported.Body)
	}
	original, err := client.StatementsNamespace.OriginalStatement(ctx, "1VgEh72lXvTXkH")
	if err != nil {
		t.Fatalf("OriginalStatement: %v", err)
	}
	if original.Filename != "M\u00e4rz.csv" || original.ContentType != "application/octet-stream" || string(original.Body) != string(statement.Reader) {
		t.Errorf("OriginalStatement = %+v (%q)", original, original.Body)
	}

	// A file its field does not admit is the SDK's error at its field.
	_, err = client.StatementsNamespace.ImportStatement(ctx, types.ImportStatementInput{AccountId: account},
		namespaces.StatementsImportStatementFiles{Statement: runtime.UploadFile{Filename: "march.xlsx", ContentType: "text/csv", Reader: []byte("x")}})
	var apiError *sdk.APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != 400 || !strings.Contains(string(apiError.Body), "uploadExtension") {
		t.Fatalf("an xlsx statement: %v", err)
	}
}
`
