package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Google's OTLP endpoint (D67, amended): Cloud Trace takes OTLP over HTTP
// at telemetry.googleapis.com, from a caller that sends an OAuth access
// token, which expires within the hour, so a static
// OTEL_EXPORTER_OTLP_HEADERS cannot carry it. When the traces endpoint is
// an https URL whose host is googleapis.com or under it, and neither
// headers variable names an authorization header, the exporter sends the
// access token of the workload's own account, which the metadata server
// gives on Cloud Run (GCE_METADATA_HOST when set, else
// metadata.google.internal), with a token fetched again five minutes
// before it expires. The token goes over https alone, and to the
// endpoint's own host alone: a redirect elsewhere carries none. An
// export that cannot get a token fails, and the SDK reports it as any
// other failed export.
//
// The exporter then sends through a client of the package's own, which
// takes OTEL_EXPORTER_OTLP_TIMEOUT and its traces form as its timeout but
// reads no OTEL_EXPORTER_OTLP_CERTIFICATE, client certificate or client
// key: Google's endpoint presents a public certificate and asks for none.

// The variables the exporter reads its headers from, which may carry an
// authorization header of their own.
const (
	HeadersVariable       = "OTEL_EXPORTER_OTLP_HEADERS"
	TracesHeadersVariable = "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
)

// The variables the timeout of an export to Google's endpoint is read
// from, in milliseconds.
const (
	TimeoutVariable       = "OTEL_EXPORTER_OTLP_TIMEOUT"
	TracesTimeoutVariable = "OTEL_EXPORTER_OTLP_TRACES_TIMEOUT"
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
// token: whether endpoint is an https URL whose host is googleapis.com or
// under it, and no headers variable names an authorization header.
func googleAuth(endpoint string, getenv func(string) string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
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

// googleTokenTransport sends each request through base, one to host over
// https with the account's access token, which it holds until five
// minutes before it expires, and any other, such as a redirect to another
// host, as it is.
type googleTokenTransport struct {
	base     http.RoundTripper
	host     string
	tokenURL string
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// newGoogleTokenTransport is a googleTokenTransport over base, nil for
// http.DefaultTransport, for the endpoint's host (host[:port], as a
// request's URL names it), that asks the metadata server metadataHost
// (host[:port]), or GCE_METADATA_HOST's when it is empty.
func newGoogleTokenTransport(base http.RoundTripper, host, metadataHost string) *googleTokenTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if metadataHost == "" {
		metadataHost = strings.TrimSpace(os.Getenv(metadataHostVariable))
	}
	if metadataHost == "" {
		metadataHost = "metadata.google.internal"
	}
	return &googleTokenTransport{base: base, host: host, tokenURL: "http://" + metadataHost + googleTokenPath, now: time.Now}
}

// RoundTrip sends req with the access token when it goes to the
// endpoint's host over https, and as it is otherwise.
func (g *googleTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Scheme, "https") || !strings.EqualFold(req.URL.Host, g.host) {
		return g.base.RoundTrip(req)
	}
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

// endpointHost is the host[:port] of endpoint, as a request's URL names
// it, or "".
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Host
}

// exportTimeoutOf is the timeout of one export: OTEL_EXPORTER_OTLP_TRACES_TIMEOUT,
// else OTEL_EXPORTER_OTLP_TIMEOUT, in milliseconds, else ten seconds,
// OpenTelemetry's default, as the exporter reads them for its own client.
func exportTimeoutOf(getenv func(string) string) time.Duration {
	for _, name := range []string{TracesTimeoutVariable, TimeoutVariable} {
		if ms, err := strconv.ParseInt(strings.TrimSpace(getenv(name)), 10, 64); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultExportTimeout
}
