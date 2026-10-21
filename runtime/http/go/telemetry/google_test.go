package telemetry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGoogleAuthGoesToGoogleAlone (D67, amended): export sends the
// account's access token to an endpoint under googleapis.com alone, and
// not when a headers variable names an authorization header of its own.
func TestGoogleAuthGoesToGoogleAlone(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}
	none := env(nil)
	for endpoint, want := range map[string]bool{
		"https://telemetry.googleapis.com":                  true,
		"https://telemetry.googleapis.com:443/v1/traces":    true,
		"https://TELEMETRY.GOOGLEAPIS.COM":                  true,
		"https://telemetry.us-central1.rep.googleapis.com/": true,
		"https://googleapis.com":                            true,
		"http://collector.internal:4318":                    false,
		"https://notgoogleapis.com":                         false,
		"https://googleapis.com.example.com":                false,
	} {
		assert.Equal(t, want, googleAuth(endpoint, none), endpoint)
	}
	for _, vars := range []map[string]string{
		{HeadersVariable: "Authorization=Bearer%20abc"},
		{TracesHeadersVariable: "x-a=1, authorization = Basic%20x"},
	} {
		assert.False(t, googleAuth("https://telemetry.googleapis.com", env(vars)), "%v", vars)
	}
	assert.True(t, googleAuth("https://telemetry.googleapis.com", env(map[string]string{HeadersVariable: "x-goog-user-project=p"})))
}

// TestGoogleTokenTransport (D67, amended): each request goes with the
// metadata server's access token, asked for with Metadata-Flavor: Google,
// held until five minutes before it expires and then fetched again; a
// metadata server that fails fails the export, which sends nothing.
func TestGoogleTokenTransport(t *testing.T) {
	var fetched atomic.Int32
	var failing atomic.Bool
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != googleTokenPath || r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "not the token path", http.StatusNotFound)
			return
		}
		if failing.Load() {
			http.Error(w, "no account", http.StatusInternalServerError)
			return
		}
		n := fetched.Add(1)
		_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":3599,"token_type":"Bearer"}`, n)
	}))
	defer metadata.Close()
	var got []string
	collector := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
	}))
	defer collector.Close()

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	transport := newGoogleTokenTransport(nil, strings.TrimPrefix(metadata.URL, "http://"))
	transport.now = func() time.Time { return now }
	client := &http.Client{Transport: transport}
	post := func() error {
		resp, err := client.Post(collector.URL+"/v1/traces", "application/x-protobuf", strings.NewReader("spans"))
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	require.NoError(t, post())
	require.NoError(t, post())
	now = now.Add(54 * time.Minute)
	require.NoError(t, post())
	now = now.Add(time.Minute)
	require.NoError(t, post())
	assert.Equal(t, []string{"Bearer token-1", "Bearer token-1", "Bearer token-1", "Bearer token-2"}, got)

	failing.Store(true)
	now = now.Add(time.Hour)
	err := post()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the metadata server answered 500")
	assert.Len(t, got, 4, "an export without a token sends nothing")
}

// TestGCEMetadataHostNamesTheMetadataServer: GCE_METADATA_HOST names the
// metadata server, as Google's client libraries read it.
func TestGCEMetadataHostNamesTheMetadataServer(t *testing.T) {
	t.Setenv(metadataHostVariable, "127.0.0.1:8089")
	assert.Equal(t, "http://127.0.0.1:8089"+googleTokenPath, newGoogleTokenTransport(nil, "").tokenURL)
	t.Setenv(metadataHostVariable, "")
	assert.Equal(t, "http://metadata.google.internal"+googleTokenPath, newGoogleTokenTransport(nil, "").tokenURL)
}
