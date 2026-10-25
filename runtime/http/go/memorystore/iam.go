package memorystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// A store whose connection's auth is iam authenticates each new
// connection as the workload's own account: the password is an OAuth
// access token the metadata server gives (GCE_METADATA_HOST when set, else
// metadata.google.internal), and the user `default`. The token is held
// until five minutes before it expires, then fetched again, so a new
// connection never sends one about to expire. A connection that opened
// keeps the token it sent: the server checks a token when a connection
// authenticates.

// MetadataHostVariable names the metadata server in place of
// metadata.google.internal, as Google's client libraries read it.
const MetadataHostVariable = "GCE_METADATA_HOST"

// MetadataTokenPath is the metadata server's path of the account's access
// token.
const MetadataTokenPath = "/computeMetadata/v1/instance/service-accounts/default/token"

// IAMUser is the user a token authenticates as.
const IAMUser = "default"

// tokenMargin is how long before it expires a token is fetched again.
const tokenMargin = 5 * time.Minute

// maxTokenBytes bounds the metadata server's answer.
const maxTokenBytes = 64 << 10

// MetadataTokens gives the workload's access token from the metadata
// server. It is safe for concurrent use.
type MetadataTokens struct {
	client *http.Client
	url    string
	now    func() time.Time
	// onToken sees each new token, for RedactingLog.AddSecret.
	onToken func(string)

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewMetadataTokens asks the metadata server host (host[:port]), or
// GCE_METADATA_HOST's, or metadata.google.internal, through client, nil
// for one with a two-second timeout. onToken, when set, sees each token it
// fetches.
func NewMetadataTokens(client *http.Client, host string, onToken func(string)) *MetadataTokens {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	if host == "" {
		host = strings.TrimSpace(os.Getenv(MetadataHostVariable))
	}
	if host == "" {
		host = "metadata.google.internal"
	}
	return &MetadataTokens{client: client, url: "http://" + host + MetadataTokenPath, now: time.Now, onToken: onToken}
}

// SetClock sets the clock a token's expiry is measured against.
func (m *MetadataTokens) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// Credentials are the user and password of a new connection: IAMUser and
// the access token. Its signature is a Redis client's per-connection
// credentials provider's.
func (m *MetadataTokens) Credentials(ctx context.Context) (username, password string, err error) {
	token, err := m.Token(ctx)
	if err != nil {
		return "", "", err
	}
	return IAMUser, token, nil
}

// Token returns the token held, or a new one when it expires within five
// minutes. An error never holds a token.
func (m *MetadataTokens) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && m.now().Add(tokenMargin).Before(m.expires) {
		return m.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url, nil)
	if err != nil {
		return "", fmt.Errorf("memorystore: the access token: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("memorystore: the access token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBytes))
	if err != nil {
		return "", fmt.Errorf("memorystore: the access token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("memorystore: the access token: the metadata server answered %d", resp.StatusCode)
	}
	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", errors.New("memorystore: the access token: the metadata server's answer is no token")
	}
	if answer.AccessToken == "" {
		return "", errors.New("memorystore: the access token: the metadata server answered no token")
	}
	m.token = answer.AccessToken
	m.expires = m.now().Add(time.Duration(answer.ExpiresIn) * time.Second)
	if m.onToken != nil {
		m.onToken(m.token)
	}
	return m.token, nil
}
