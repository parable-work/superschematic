package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Google's OTLP endpoint (D67, amended): Cloud Trace takes OTLP over HTTP
// at telemetry.googleapis.com, from a caller that sends an OAuth access
// token, which expires within the hour, so a static
// OTEL_EXPORTER_OTLP_HEADERS cannot carry it. When the traces endpoint's
// host is googleapis.com or under it, and neither headers variable names an
// authorization header, the exporter sends the access token of the
// workload's own account, which the metadata server gives on Cloud Run
// (GCE_METADATA_HOST when set, else metadata.google.internal), each
// request with a token fetched again five minutes before it expires. An
// export that cannot get a token fails, and the SDK reports it as any
// other failed export.

// The variables the exporter reads its headers from, which may carry an
// authorization header of their own.
const (
	HeadersVariable       = "OTEL_EXPORTER_OTLP_HEADERS"
	TracesHeadersVariable = "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
)

// metadataHostVariable names the metadata server in place of
// metadata.google.internal, as Google's client libraries read it.
const metadataHostVariable = "GCE_METADATA_HOST"

// googleTokenPath is the metadata server's path for the account's access
// token.
const googleTokenPath = "/computeMetadata/v1/instance/service-accounts/default/token"

// googleTokenMargin is how long before it expires an access token is
// fetched again.
const googleTokenMargin = 5 * time.Minute

// maxGoogleTokenBytes bounds the metadata server's answer.
const maxGoogleTokenBytes = 64 << 10

// googleAuth reports whether export to endpoint sends the account's access
// token: whether endpoint's host is googleapis.com or under it, and no
// headers variable names an authorization header.
func googleAuth(endpoint string, getenv func(string) string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "googleapis.com" && !strings.HasSuffix(host, ".googleapis.com") {
		return false
	}
	for _, name := range []string{TracesHeadersVariable, HeadersVariable} {
		for _, pair := range strings.Split(getenv(name), ",") {
			key, _, _ := strings.Cut(pair, "=")
			if key, err := url.PathUnescape(strings.TrimSpace(key)); err == nil && strings.EqualFold(key, "authorization") {
				return false
			}
		}
	}
	return true
}

// googleTokenTransport sends each request through base with the
// account's access token, which it holds until five minutes before it
// expires.
type googleTokenTransport struct {
	base     http.RoundTripper
	tokenURL string
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// newGoogleTokenTransport is a googleTokenTransport over base, nil for
// http.DefaultTransport, that asks the metadata server metadataHost
// (host[:port]), or GCE_METADATA_HOST's when it is empty.
func newGoogleTokenTransport(base http.RoundTripper, metadataHost string) *googleTokenTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if metadataHost == "" {
		metadataHost = strings.TrimSpace(os.Getenv(metadataHostVariable))
	}
	if metadataHost == "" {
		metadataHost = "metadata.google.internal"
	}
	return &googleTokenTransport{base: base, tokenURL: "http://" + metadataHost + googleTokenPath, now: time.Now}
}

// RoundTrip sends req with the access token.
func (g *googleTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := g.accessToken(req)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+token)
	return g.base.RoundTrip(out)
}

// accessToken returns the token held, or a new one from the metadata
// server when it expires within the margin.
func (g *googleTokenTransport) accessToken(req *http.Request) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && g.now().Add(googleTokenMargin).Before(g.expires) {
		return g.token, nil
	}
	get, err := http.NewRequestWithContext(req.Context(), http.MethodGet, g.tokenURL, nil)
	if err != nil {
		return "", err
	}
	get.Header.Set("Metadata-Flavor", "Google")
	resp, err := g.base.RoundTrip(get)
	if err != nil {
		return "", fmt.Errorf("telemetry: the access token for %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGoogleTokenBytes))
	if err != nil {
		return "", fmt.Errorf("telemetry: the access token for %s: %w", req.URL.Host, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("telemetry: the access token for %s: the metadata server answered %d: %s", req.URL.Host, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", fmt.Errorf("telemetry: the access token for %s: %w", req.URL.Host, err)
	}
	if answer.AccessToken == "" {
		return "", errors.New("telemetry: the metadata server answered no access token")
	}
	g.token = answer.AccessToken
	g.expires = g.now().Add(time.Duration(answer.ExpiresIn) * time.Second)
	return g.token, nil
}
