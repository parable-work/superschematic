package gosdkgen

import "testing"

// TestClientVersionAndItsRefusal runs clientVersionSDKTest in the generated
// SDK (D143): SDKConfig.Client puts X-Client-Version on every call, a
// client without one sends no such header, a name or a version the header
// cannot carry is refused where the client is built, and a 426
// client_too_old is a *ClientTooOldError with the client, its version and
// the minimum.
func TestClientVersionAndItsRefusal(t *testing.T) {
	runInNestedArraysSDK(t, "client_version_test.go", clientVersionSDKTest)
}

// clientVersionSDKTest runs in the generated SDK module against an httptest
// server that records each request's X-Client-Version.
const clientVersionSDKTest = `package sdk_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-nested-arrays-api"
)

const gridID = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"

func TestClientVersionHeader(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, strings.Join(r.Header.Values("X-Client-Version"), "|"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"data\":[],\"meta\":{\"requestId\":\"req-1\"}}")
	}))
	t.Cleanup(server.Close)

	named, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL, Client: &sdk.ClientIdentity{Name: "acme-cli", Version: "0.5.0-rc.1+b7"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := named.GridNamespace.GridLabels(context.Background(), gridID, nil); err != nil {
			t.Fatal(err)
		}
	}
	unnamed, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unnamed.GridNamespace.GridLabels(context.Background(), gridID, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, ","); got != "acme-cli/0.5.0-rc.1+b7,acme-cli/0.5.0-rc.1+b7," {
		t.Errorf("X-Client-Version by request = %q", got)
	}

	for _, client := range []sdk.ClientIdentity{
		{Name: "", Version: "1.0.0"},
		{Name: "acme/cli", Version: "1.0.0"},
		{Name: "acme cli", Version: "1.0.0"},
		{Name: "-cli", Version: "1.0.0"},
		{Name: "cli", Version: ""},
		{Name: "cli", Version: "1.0.0\r\nX-Other: 1"},
		{Name: "cli", Version: "1.0/0"},
	} {
		if _, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL, Client: &client}); err == nil || !strings.Contains(err.Error(), "Client.") {
			t.Errorf("client %+v: %v, want a config error that names the member", client, err)
		}
	}
}

func TestClientTooOld(t *testing.T) {
	status, body := http.StatusUpgradeRequired, "{\"type\":\"about:blank\",\"title\":\"Upgrade Required\",\"status\":426,\"detail\":\"cli 0.4.0 is older than 0.5.0\",\"code\":\"client_too_old\",\"requestId\":\"req-9\",\"details\":{\"client\":\"cli\",\"version\":\"0.4.0\",\"minimum\":\"0.5.0\"}}"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL, Client: &sdk.ClientIdentity{Name: "cli", Version: "0.4.0"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GridNamespace.GridLabels(context.Background(), gridID, nil)
	var tooOld *sdk.ClientTooOldError
	if !errors.As(err, &tooOld) {
		t.Fatalf("error = %v, want a *ClientTooOldError", err)
	}
	if tooOld.Client != "cli" || tooOld.Version != "0.4.0" || tooOld.Minimum != "0.5.0" || tooOld.StatusCode != 426 || tooOld.Code != "client_too_old" || tooOld.Message != "cli 0.4.0 is older than 0.5.0" {
		t.Errorf("ClientTooOldError = %+v (%+v)", tooOld, tooOld.APIError)
	}
	var apiError *sdk.APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != 426 {
		t.Errorf("errors.As finds no *APIError in %v", err)
	}

	// A 426 with another code is the plain error.
	body = "{\"status\":426,\"detail\":\"use TLS\",\"code\":\"upgrade_required\"}"
	_, err = client.GridNamespace.GridLabels(context.Background(), gridID, nil)
	if errors.As(err, &tooOld) || !errors.As(err, &apiError) || apiError.StatusCode != 426 {
		t.Errorf("another 426 = %v, want an *APIError alone", err)
	}
}
`
