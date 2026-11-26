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

const streamService = "fixture-stream-api"

// TestBinaryStreamThroughTheGoSDK generates the Go types module, the Go API
// module and the Go SDK of fixture-stream-api into one temp tree, then runs
// streamServerTest in the API module. The generated router mounts the
// @manualRouteRegistration handler of exportReport, an @httpBinaryStream
// operation (D167), which streams an Arrow IPC body in chunks with a
// header; the SDK method returns that stream unread, a refusal as the
// SDK's APIError, and a close before the end ends the request.
func TestBinaryStreamThroughTheGoSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join("..", "..", "loader", "tsreader", "testdata", "services", streamService))
	if err != nil {
		t.Fatalf("load %s: %v", streamService, err)
	}
	paths := testpaths.Local(t)
	typesModule := "example.com/schemas/types/go/" + streamService
	sdkModule := "example.com/schemas/sdk/go/" + streamService

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", streamService)
	apiDir := filepath.Join(root, "api", streamService)
	sdkDir := filepath.Join(root, "sdk", "go", streamService)

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: streamService, ModulePath: typesModule, Clock: nestedArraysClock})
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
		SchemaName:  streamService,
		ModulePath:  "example.com/schemas/api/" + streamService,
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

	if err := os.WriteFile(filepath.Join(apiDir, "stream_sdk_test.go"), []byte(streamServerTest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"mod", "edit", "-replace", sdkModule + "=../../sdk/go/" + streamService},
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

// streamServerTest runs in the generated API module of fixture-stream-api,
// with the Go SDK replaced in.
const streamServerTest = `package fixturestreamapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-stream-api"
	sdk "example.com/schemas/sdk/go/fixture-stream-api"
	"example.com/schemas/sdk/go/fixture-stream-api/namespaces"
	"example.com/schemas/sdk/go/fixture-stream-api/runtime"
	types "example.com/schemas/types/go/fixture-stream-api"
)

const arrowStream = "application/vnd.apache.arrow.stream"

// arrowBody is shaped like an Arrow IPC stream: each message starts with
// the 0xFFFFFFFF continuation marker, which no JSON decoder reads, and the
// stream ends with the end marker.
func arrowBody() []byte {
	var body []byte
	for i := 0; i < 64; i++ {
		body = append(body, 0xff, 0xff, 0xff, 0xff, byte(i), 0, 0, 0)
		body = append(body, bytes.Repeat([]byte{byte(i)}, 1024)...)
	}
	return append(body, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0)
}

type reports struct{}

func (reports) DescribeReport(_ context.Context, reportID string) (*types.ReportSummary, error) {
	return &types.ReportSummary{ReportId: reportID, Rows: 64}, nil
}

// ExportReport is the interface method; the route is the manual handler.
func (reports) ExportReport(context.Context, string, *string, *types.ExportReportInput) (*types.ReportSummary, error) {
	return nil, errors.New("the manual route serves exportReport")
}

// serve mounts export as exportReport's handler and returns an SDK client
// of the server, whose timeout is timeoutMs (0 for the default).
func serve(t *testing.T, export http.HandlerFunc, timeoutMs int) *sdk.FixtureStreamApiSDK {
	t.Helper()
	router := chi.NewRouter()
	config := api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Reports: reports{}},
		ManualRoutes:    &api.ManualRoutes{ReportsExportReport: export},
	}
	if err := api.RegisterRoutes(router, config); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL, Timeout: timeoutMs})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

var exportInput = types.ExportReportInput{Columns: []string{"region", "total"}}

func TestExportReportReturnsTheStreamedBody(t *testing.T) {
	want := arrowBody()
	var accept, format, reportID string
	var sent map[string]any
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		accept, format, reportID = r.Header.Get("Accept"), r.URL.Query().Get("format"), chi.URLParam(r, "reportId")
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", arrowStream)
		w.Header().Set("X-Export-Id", "export-7")
		w.WriteHeader(http.StatusOK)
		for start := 0; start < len(want); start += 4096 {
			_, _ = w.Write(want[start:min(start+4096, len(want))])
			w.(http.Flusher).Flush()
		}
	}, 0)

	arrow := "arrow"
	var stream *runtime.BinaryStream
	stream, err := client.ReportsNamespace.ExportReport(context.Background(), "r-1", exportInput, &namespaces.ReportsExportReportQueryParams{Format: &arrow})
	if err != nil {
		t.Fatalf("ExportReport: %v", err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream.Body)
	if err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close the stream: %v", err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("read %d bytes, want the %d the handler streamed", len(body), len(want))
	}
	if stream.ContentType != arrowStream || stream.Header.Get("X-Export-Id") != "export-7" {
		t.Errorf("ContentType = %q, X-Export-Id = %q", stream.ContentType, stream.Header.Get("X-Export-Id"))
	}
	if accept != arrowStream+", application/problem+json" || format != "arrow" || reportID != "r-1" {
		t.Errorf("Accept = %q, format = %q, reportId = %q", accept, format, reportID)
	}
	if columns, _ := sent["columns"].([]any); len(columns) != 2 {
		t.Errorf("request body = %v, want the input's two columns", sent)
	}

	// The JSON operation beside it still reads the envelope.
	summary, err := client.ReportsNamespace.DescribeReport(context.Background(), "r-1")
	if err != nil || summary.ReportId != "r-1" || summary.Rows != 64 {
		t.Fatalf("DescribeReport = %+v, %v", summary, err)
	}
}

func TestExportReportRefusalIsTheAPIError(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("X-Request-ID", "req-1")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(` + "`" + `{"type":"about:blank","title":"Bad Request","status":400,"detail":"the report has no such column","code":"unknown_column"}` + "`" + `))
	}, 0)

	stream, err := client.ReportsNamespace.ExportReport(context.Background(), "r-1", exportInput, nil)
	if stream != nil {
		t.Fatal("a refusal returned a stream")
	}
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %v, want *sdk.APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "the report has no such column" || apiErr.Code != "unknown_column" || apiErr.RequestID != "req-1" {
		t.Errorf("APIError = %+v", apiErr)
	}
}

func TestExportReportRefusesAnotherContentType(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(` + "`" + `{"data":{},"meta":{"requestId":"req-1"}}` + "`" + `))
	}, 0)

	if _, err := client.ReportsNamespace.ExportReport(context.Background(), "r-1", exportInput, nil); !errors.Is(err, sdk.ErrStreamContentType) {
		t.Fatalf("err = %v, want ErrStreamContentType", err)
	}
}

// A close before the end returns at once and ends the request: the
// handler, which writes until its request is done, sees it end.
func TestExportReportCloseBeforeTheEndEndsTheRequest(t *testing.T) {
	ended := make(chan struct{})
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(ended)
		w.Header().Set("Content-Type", arrowStream)
		w.WriteHeader(http.StatusOK)
		for {
			if _, err := w.Write(bytes.Repeat([]byte{0xff}, 4096)); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}, 0)

	stream, err := client.ReportsNamespace.ExportReport(context.Background(), "r-1", exportInput, nil)
	if err != nil {
		t.Fatalf("ExportReport: %v", err)
	}
	if _, err := io.ReadFull(stream.Body, make([]byte, 8192)); err != nil {
		t.Fatalf("read the first bytes: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- stream.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's request did not end after Close")
	}
}

// The SDK timeout bounds the wait for the response headers, not the body:
// a stream that pauses longer than the timeout after its headers reads
// whole, and headers later than the timeout fail.
func TestExportReportTimeoutCoversTheHeadersOnly(t *testing.T) {
	const timeoutMs = 200
	pause := 3 * timeoutMs * time.Millisecond
	slowBody := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", arrowStream)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0xff, 0xff, 0xff, 0xff})
		w.(http.Flusher).Flush()
		time.Sleep(pause)
		_, _ = w.Write([]byte{0, 0, 0, 0})
	}, timeoutMs)

	stream, err := slowBody.ReportsNamespace.ExportReport(context.Background(), "r-1", exportInput, nil)
	if err != nil {
		t.Fatalf("ExportReport: %v", err)
	}
	got, err := io.ReadAll(stream.Body)
	_ = stream.Close()
	if err != nil || !bytes.Equal(got, []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}) {
		t.Fatalf("read %v, %v; want the whole body after a pause past the timeout", got, err)
	}

	slowHeaders := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(pause)
		w.Header().Set("Content-Type", arrowStream)
	}, timeoutMs)
	if _, err := slowHeaders.ReportsNamespace.ExportReport(context.Background(), "r-1", exportInput, nil); err == nil {
		t.Fatal("headers later than the timeout returned a stream")
	}
}
`
