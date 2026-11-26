// This file runs inside the generated API module of router-options-api
// with go_api.manual_route_constants set (TestRouterOptionsAnswerTheirRequests
// copies it there only then): it mounts the @manualRouteRegistration route
// itself from the constants routes.go writes.
package routeroptionsapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	api "example.com/schemas/api/router-options-api"
)

// TestManualRouteConstants: the constants carry the operation's method,
// full path, rate limit and its counter's name, body limit and timeout,
// and a server mounts the route at them.
func TestManualRouteConstants(t *testing.T) {
	if api.WidgetUploadWidgetFileHandlerMethod != http.MethodPut || api.WidgetUploadWidgetFileHandlerPath != "/api/widgets/{id}/file" {
		t.Fatalf("method and path = %s %s", api.WidgetUploadWidgetFileHandlerMethod, api.WidgetUploadWidgetFileHandlerPath)
	}
	if api.WidgetUploadWidgetFileHandlerRequestsPerMinute != 30 || api.WidgetUploadWidgetFileHandlerRateLimitBucket != "PUT /api/widgets/{id}/file" {
		t.Errorf("rate limit = %d, %q", api.WidgetUploadWidgetFileHandlerRequestsPerMinute, api.WidgetUploadWidgetFileHandlerRateLimitBucket)
	}
	if api.WidgetUploadWidgetFileHandlerBodyLimitMegabytes != 8 || api.WidgetUploadWidgetFileHandlerTimeoutSeconds != 60 {
		t.Errorf("body limit %d, timeout %d", api.WidgetUploadWidgetFileHandlerBodyLimitMegabytes, api.WidgetUploadWidgetFileHandlerTimeoutSeconds)
	}
	r := chi.NewRouter()
	r.Method(api.WidgetUploadWidgetFileHandlerMethod, api.WidgetUploadWidgetFileHandlerPath, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/widgets/w1/file", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("the mounted route answered %d", rec.Code)
	}
}
