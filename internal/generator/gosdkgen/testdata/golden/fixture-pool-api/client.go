package sdk

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"example.com/schemas/sdk/go/fixture-pool-api/runtime"
	types "example.com/schemas/types/go/fixture-pool-api"
)

type HTTPClient struct {
	httpClient *http.Client
	// streamClient shares httpClient's transport without its total timeout,
	// so a stream's body can outlast it. See DoStream.
	streamClient *http.Client
	timeout      time.Duration
	config       SDKConfig
	baseURL      string
	// clientVersion is the X-Client-Version every call sends (D143), empty
	// without config.Client.
	clientVersion string

	tokenMu sync.RWMutex
	token   string

	refreshMu    sync.Mutex
	refreshState *tokenRefreshState
}

type tokenRefreshState struct {
	done  chan struct{}
	token string
	err   error
}

const maxRateLimitSleepSeconds = 300

var (
	// ErrEnvelopeContract matches any strict success-envelope contract failure.
	ErrEnvelopeContract = errors.New("SDK contract error: invalid RFC 9457 envelope in success response")
	// ErrEnvelopeContractObject indicates the response body is not a JSON object envelope.
	ErrEnvelopeContractObject = errors.New("SDK contract error: expected RFC 9457 envelope object with data/meta.requestId in success response")
	// ErrEnvelopeContractFields indicates data/meta envelope fields are missing.
	ErrEnvelopeContractFields = errors.New("SDK contract error: expected RFC 9457 envelope fields data and meta in success response")
	// ErrEnvelopeContractMetaRequestID indicates meta.requestId is missing or malformed.
	ErrEnvelopeContractMetaRequestID = errors.New("SDK contract error: expected RFC 9457 envelope meta.requestId in success response")
	// ErrStreamContentType indicates a streamed success response is not the
	// media type the operation declares (D167).
	ErrStreamContentType = errors.New("SDK contract error: unexpected content type in streamed success response")
)

// EnvelopeContractError describes strict success-envelope contract violations.
// Use errors.Is(err, ErrEnvelopeContract*) and errors.As(err, *EnvelopeContractError)
// to detect and inspect unwrap failures programmatically.
type EnvelopeContractError struct {
	Reason error
	Cause  error
}

func (e *EnvelopeContractError) Error() string {
	if e == nil {
		return ErrEnvelopeContract.Error()
	}
	message := ErrEnvelopeContract.Error()
	if e.Reason != nil {
		message = e.Reason.Error()
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", message, e.Cause)
	}
	return message
}

func (e *EnvelopeContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *EnvelopeContractError) Is(target error) bool {
	if e == nil || target == nil {
		return false
	}
	if target == ErrEnvelopeContract {
		return true
	}
	return e.Reason != nil && errors.Is(e.Reason, target)
}

func newEnvelopeContractError(reason error, cause error) error {
	return &EnvelopeContractError{
		Reason: reason,
		Cause:  cause,
	}
}

func NewHTTPClient(config SDKConfig) (*HTTPClient, error) {
	if config.Auth != nil && config.Auth.GetToken != nil && config.Auth.TokenProvider != nil {
		return nil, fmt.Errorf("AuthConfig: GetToken and TokenProvider are mutually exclusive; use GetToken (TokenProvider is deprecated)")
	}

	timeout := 30 * time.Second
	if config.Timeout > 0 {
		timeout = time.Duration(config.Timeout) * time.Millisecond
	}

	baseURL := strings.TrimRight(config.BaseURL, "/")
	token := ""
	if config.Auth != nil {
		token = strings.TrimSpace(config.Auth.Token)
	}

	httpClient := &http.Client{Timeout: timeout}
	if config.Transport != nil {
		httpClient.Transport = config.Transport
	}

	clientVersion, err := clientVersionHeader(config.Client)
	if err != nil {
		return nil, err
	}

	return &HTTPClient{
		httpClient:    httpClient,
		streamClient:  &http.Client{Transport: httpClient.Transport},
		timeout:       timeout,
		config:        config,
		baseURL:       baseURL,
		clientVersion: clientVersion,
		token:         token,
	}, nil
}

var (
	clientNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	clientVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}$`)
)

// clientVersionHeader is the X-Client-Version header of a client (D143),
// `<name>/<version>`, or "" for none. A name or a version the header
// cannot carry is an error, so a typo is found where the SDK is built.
func clientVersionHeader(client *ClientIdentity) (string, error) {
	if client == nil {
		return "", nil
	}
	if !clientNamePattern.MatchString(client.Name) {
		return "", fmt.Errorf("SDKConfig: Client.Name %q must start with a letter or a digit and hold letters, digits, '.', '_' and '-', 64 characters at most", client.Name)
	}
	if !clientVersionPattern.MatchString(client.Version) {
		return "", fmt.Errorf("SDKConfig: Client.Version %q must start with a letter or a digit and hold letters, digits, '.', '+', '_' and '-', 128 characters at most", client.Version)
	}
	return client.Name + "/" + client.Version, nil
}

func (c *HTTPClient) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	if c.config.Encryption == nil {
		c.config.Encryption = &EncryptionConfig{}
	}
	c.config.Encryption.PublicEncryptionKey = key
}

func (c *HTTPClient) SetToken(token string) {
	normalizedToken := strings.TrimSpace(token)
	c.setStaticToken(normalizedToken)
	if storage := c.tokenStorage(); storage != nil {
		if normalizedToken == "" {
			storage.ClearToken(context.Background())
			return
		}
		storage.SetToken(context.Background(), normalizedToken)
	}
}

func (c *HTTPClient) ClearToken() {
	c.setStaticToken("")
	if storage := c.tokenStorage(); storage != nil {
		storage.ClearToken(context.Background())
	}
}

func (c *HTTPClient) DoJSON(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	out any,
) error {
	statusCode, responseBody, responseHeaders, err := c.do(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	if statusCode >= 400 {
		return decodeError(statusCode, responseBody, responseHeaders)
	}
	if out == nil || len(responseBody) == 0 {
		return nil
	}
	responseBody, err = unwrapEnvelope(responseBody)
	if err != nil {
		return fmt.Errorf("unwrap response envelope: %w", err)
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode response body: %w", err)
	}
	return nil
}

// DoFile sends a request whose success body is a file, a FileDownload
// result (D81), not the envelope: it returns the bytes, the filename
// Content-Disposition offered and the Content-Type. A refusal is the error
// DoJSON would return.
func (c *HTTPClient) DoFile(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
) (*runtime.DownloadedFile, error) {
	statusCode, responseBody, responseHeaders, err := c.do(ctx, method, path, query, body)
	if err != nil {
		return nil, err
	}
	if statusCode >= 400 {
		return nil, decodeError(statusCode, responseBody, responseHeaders)
	}
	return &runtime.DownloadedFile{
		Body:        responseBody,
		Filename:    runtime.FilenameOf(responseHeaders.Get("Content-Disposition")),
		ContentType: responseHeaders.Get("Content-Type"),
	}, nil
}

// DoStream sends a request whose success body is a stream of contentType,
// an @httpBinaryStream operation's (D167), and returns that body unread:
// the caller reads it and must close it. A refusal is decoded into the
// errors DoJSON returns. A success answer of another media type is
// ErrStreamContentType, with the body closed. Timeout bounds the wait for
// the response headers, not the body: ctx and closing the body bound
// reading it. ResponseInterceptor is not applied, since it takes the
// whole body.
func (c *HTTPClient) DoStream(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	contentType string,
) (*runtime.BinaryStream, error) {
	requestBody, requestContentType, err := c.buildRequestBody(body)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, method, c.requestURL(path, query), requestBody, requestContentType, contentType+", application/problem+json", true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		responseHeaders := resp.Header.Clone()
		responseBody, err := c.readResponseBody(resp)
		if err != nil {
			return nil, err
		}
		return nil, decodeError(resp.StatusCode, responseBody, responseHeaders)
	}
	got := resp.Header.Get("Content-Type")
	if mediaType, _, err := mime.ParseMediaType(got); err != nil || mediaType != contentType {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: want %s, got %q (status %d)", ErrStreamContentType, contentType, got, resp.StatusCode)
	}
	return &runtime.BinaryStream{
		ContentType: contentType,
		Header:      resp.Header.Clone(),
		Body:        resp.Body,
	}, nil
}

// unwrapEnvelope enforces the success response contract and returns
// the inner data payload from {data, meta: {requestId}}.
// SYNC: keep strict envelope validation in sync with
// runtime/http/go/response/unwrap.go.
func unwrapEnvelope(body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, newEnvelopeContractError(ErrEnvelopeContractObject, err)
	}
	dataRaw, hasData := envelope["data"]
	metaRaw, hasMeta := envelope["meta"]
	if !hasData || !hasMeta {
		return nil, newEnvelopeContractError(ErrEnvelopeContractFields, nil)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return nil, newEnvelopeContractError(ErrEnvelopeContractMetaRequestID, err)
	}
	if _, hasRequestID := meta["requestId"]; !hasRequestID {
		return nil, newEnvelopeContractError(ErrEnvelopeContractMetaRequestID, nil)
	}
	return dataRaw, nil
}

func (c *HTTPClient) maxRateLimitRetries() int {
	if c.config.MaxRateLimitRetries != nil {
		return *c.config.MaxRateLimitRetries
	}
	return 3
}

func (c *HTTPClient) do(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
) (int, []byte, http.Header, error) {
	requestBody, contentType, err := c.buildRequestBody(body)
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := c.send(ctx, method, c.requestURL(path, query), requestBody, contentType, "application/json", false)
	if err != nil {
		return 0, nil, nil, err
	}
	responseHeaders := resp.Header.Clone()
	responseBody, err := c.readResponseBody(resp)
	if err != nil {
		return 0, nil, nil, err
	}

	if interceptor := c.config.ResponseInterceptor; interceptor != nil && resp.StatusCode < http.StatusBadRequest {
		interceptedBody, err := interceptor(ctx, resp, responseBody)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("response interceptor: %w", err)
		}
		if interceptedBody != nil {
			responseBody = interceptedBody
		}
	}

	return resp.StatusCode, responseBody, responseHeaders, nil
}

func (c *HTTPClient) requestURL(path string, query url.Values) string {
	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	return requestURL
}

// send sends one request and returns the last response with its body
// unread. It retries once after refreshing an expired end-user token, once
// with a fresh service credential, and up to maxRateLimitRetries times
// after a 429. A stream request goes through streamClient, with Timeout a
// deadline on the response headers only.
func (c *HTTPClient) send(
	ctx context.Context,
	method string,
	requestURL string,
	requestBody []byte,
	contentType string,
	accept string,
	stream bool,
) (*http.Response, error) {
	// One call refreshes the end user at most once and asks for a fresh
	// service credential at most once.
	alreadyRetriedAuth := false
	serviceRetried := false
	freshServiceToken := false
	rateLimitAttempt := 0
	for {
		req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(requestBody))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", accept)
		if c.clientVersion != "" {
			req.Header.Set("X-Client-Version", c.clientVersion)
		}
		if key, ok := runtime.CallIdempotencyKey(ctx); ok {
			// A call to an @idempotent operation: each send carries the
			// call's one key (D140).
			req.Header.Set("Idempotency-Key", key)
		}

		if err := c.applyAuth(ctx, req); err != nil {
			return nil, err
		}
		if err := c.applyServiceCredential(ctx, req, freshServiceToken); err != nil {
			return nil, err
		}
		freshServiceToken = false
		if interceptor := c.config.RequestInterceptor; interceptor != nil {
			if err := interceptor(ctx, req); err != nil {
				return nil, fmt.Errorf("request interceptor: %w", err)
			}
		}

		var resp *http.Response
		if stream {
			resp, err = c.sendStream(req)
		} else {
			resp, err = c.httpClient.Do(req)
		}
		if err != nil {
			return nil, &NetworkError{APIError: &APIError{
				Message: "request failed",
				Body:    []byte(err.Error()),
			}}
		}

		if resp.StatusCode == http.StatusUnauthorized {
			// A 401's body says which credential was refused. Read it here,
			// and hand it on unread to a caller that gets the response.
			responseBody, err := c.readResponseBody(resp)
			if err != nil {
				return nil, err
			}
			resp.Body = io.NopCloser(bytes.NewReader(responseBody))
			if problemCode(responseBody) == "service_unauthorized" {
				// The service credential was refused: ask its source for a
				// fresh token once. The end-user refresh cannot help, so it
				// does not run.
				if !serviceRetried && c.serviceTokenSource() != nil {
					serviceRetried = true
					freshServiceToken = true
					continue
				}
			} else if !alreadyRetriedAuth && c.refreshTokenProvider() != nil {
				if _, err := c.refreshAuthToken(ctx); err != nil {
					return nil, &AuthenticationError{APIError: &APIError{
						Message:    "authentication failed",
						StatusCode: http.StatusUnauthorized,
						Body:       []byte(err.Error()),
					}}
				}
				alreadyRetriedAuth = true
				continue
			}
		}

		if resp.StatusCode == http.StatusTooManyRequests && rateLimitAttempt < c.maxRateLimitRetries() {
			discardBody(resp)
			wait := 1 << rateLimitAttempt
			if retryAfter := parseRetryAfter(resp.Header); retryAfter != nil && *retryAfter > 0 {
				wait = *retryAfter
			}
			if wait > maxRateLimitSleepSeconds {
				wait = maxRateLimitSleepSeconds
			}
			select {
			case <-time.After(time.Duration(wait) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			rateLimitAttempt++
			continue
		}

		return resp, nil
	}
}

// sendStream sends req through streamClient and cancels it if the response
// headers take longer than Timeout. The returned body cancels the request
// when it is closed.
func (c *HTTPClient) sendStream(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(c.timeout, cancel)
	resp, err := c.streamClient.Do(req.WithContext(ctx))
	if !timer.Stop() {
		if err == nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, fmt.Errorf("no response headers within %s", c.timeout)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose is a stream's body: closing it ends the request.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// discardBody closes a response that is about to be retried.
func discardBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

func (c *HTTPClient) applyAuth(ctx context.Context, req *http.Request) error {
	token, err := c.resolveAuthToken(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}

	headerName := c.authHeaderName()

	if strings.EqualFold(headerName, "Authorization") {
		req.Header.Set(headerName, "Bearer "+token)
	} else {
		req.Header.Set(headerName, token)
	}
	return nil
}

// applyServiceCredential sends the service credential, "Bearer <token>", in
// each configured header. fresh asks the source for a new token.
func (c *HTTPClient) applyServiceCredential(ctx context.Context, req *http.Request, fresh bool) error {
	source := c.serviceTokenSource()
	if source == nil {
		return nil
	}
	token, err := source(ctx, fresh)
	if err != nil {
		return fmt.Errorf("resolve service credential: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	headers := c.config.ServiceCredential.Headers
	if len(headers) == 0 {
		headers = []string{"Service-Authorization"}
	}
	for _, name := range headers {
		req.Header.Set(name, "Bearer "+token)
	}
	return nil
}

func (c *HTTPClient) serviceTokenSource() func(ctx context.Context, fresh bool) (string, error) {
	if c.config.ServiceCredential == nil {
		return nil
	}
	return c.config.ServiceCredential.Token
}

// quoteEscaper escapes a multipart part's name and filename as
// mime/multipart's CreateFormFile does.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func (c *HTTPClient) buildRequestBody(body any) ([]byte, string, error) {
	contentType := "application/json"
	switch payload := body.(type) {
	case nil:
		return nil, contentType, nil
	case runtime.MultipartBody:
		buf := &bytes.Buffer{}
		writer := multipart.NewWriter(buf)
		if payload.JSONData != nil {
			jsonData, err := json.Marshal(payload.JSONData)
			if err != nil {
				return nil, "", fmt.Errorf("marshal multipart data: %w", err)
			}
			if err := writer.WriteField("data", string(jsonData)); err != nil {
				return nil, "", fmt.Errorf("write multipart data field: %w", err)
			}
		}
		for fieldName, file := range payload.Files {
			if len(file.Reader) == 0 {
				continue
			}
			// The part carries the file's media type, which a route checks
			// against its field (D81); application/octet-stream without one.
			partContentType := file.ContentType
			if partContentType == "" {
				partContentType = "application/octet-stream"
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, quoteEscaper.Replace(fieldName), quoteEscaper.Replace(file.Filename)))
			header.Set("Content-Type", partContentType)
			part, err := writer.CreatePart(header)
			if err != nil {
				return nil, "", fmt.Errorf("create multipart file field %q: %w", fieldName, err)
			}
			if _, err := part.Write(file.Reader); err != nil {
				return nil, "", fmt.Errorf("write multipart file field %q: %w", fieldName, err)
			}
		}
		if err := writer.Close(); err != nil {
			return nil, "", fmt.Errorf("finalize multipart body: %w", err)
		}
		return buf.Bytes(), writer.FormDataContentType(), nil
	default:
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, "", fmt.Errorf("marshal request body: %w", err)
		}
		return bodyBytes, contentType, nil
	}
}

const defaultMaxResponseSize int64 = 100 << 20 // 100 MB

func (c *HTTPClient) maxResponseSize() int64 {
	if c.config.MaxResponseSize > 0 {
		return c.config.MaxResponseSize
	}
	return defaultMaxResponseSize
}

func (c *HTTPClient) readResponseBody(resp *http.Response) ([]byte, error) {
	limited := io.LimitReader(resp.Body, c.maxResponseSize()+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return nil, fmt.Errorf("close response body: %w", err)
	}
	if int64(len(respBody)) > c.maxResponseSize() {
		return nil, fmt.Errorf("response body exceeds maximum allowed size of %d bytes", c.maxResponseSize())
	}
	return respBody, nil
}

func (c *HTTPClient) resolveAuthToken(ctx context.Context) (string, error) {
	token := c.staticToken()
	if token == "" {
		if provider := c.tokenProvider(); provider != nil {
			providedToken, err := provider(ctx)
			if err != nil {
				return "", fmt.Errorf("resolve auth token: %w", err)
			}
			token = strings.TrimSpace(providedToken)
		}
	}
	if token == "" {
		if storage := c.tokenStorage(); storage != nil {
			token = strings.TrimSpace(storage.GetToken(ctx))
		}
	}
	return token, nil
}

func (c *HTTPClient) refreshAuthToken(ctx context.Context) (string, error) {
	refreshProvider := c.refreshTokenProvider()
	if refreshProvider == nil {
		return "", fmt.Errorf("no token refresh callback configured")
	}

	c.refreshMu.Lock()
	if c.refreshState != nil {
		state := c.refreshState
		c.refreshMu.Unlock()
		select {
		case <-state.done:
			return state.token, state.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	state := &tokenRefreshState{
		done: make(chan struct{}),
	}
	c.refreshState = state
	c.refreshMu.Unlock()

	token, err := refreshProvider(ctx)
	token = strings.TrimSpace(token)
	if err == nil && token == "" {
		err = fmt.Errorf("refresh token callback returned empty token")
	}
	if err == nil {
		c.setStaticToken(token)
		if storage := c.tokenStorage(); storage != nil {
			storage.SetToken(ctx, token)
		}
	}

	c.refreshMu.Lock()
	state.token = token
	state.err = err
	close(state.done)
	c.refreshState = nil
	c.refreshMu.Unlock()

	return token, err
}

func (c *HTTPClient) tokenProvider() TokenProvider {
	if c.config.Auth == nil {
		return nil
	}
	if c.config.Auth.GetToken != nil {
		return c.config.Auth.GetToken
	}
	return c.config.Auth.TokenProvider
}

func (c *HTTPClient) refreshTokenProvider() TokenProvider {
	if c.config.Auth == nil {
		return nil
	}
	return c.config.Auth.RefreshToken
}

func (c *HTTPClient) tokenStorage() TokenStorage {
	if c.config.Auth == nil {
		return nil
	}
	return c.config.Auth.Storage
}

func (c *HTTPClient) authHeaderName() string {
	if c.config.Auth == nil {
		return "Authorization"
	}
	headerName := strings.TrimSpace(c.config.Auth.HeaderName)
	if headerName == "" {
		return "Authorization"
	}
	return headerName
}

func (c *HTTPClient) staticToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

func (c *HTTPClient) setStaticToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = strings.TrimSpace(token)
}

func (c *HTTPClient) EncryptRequestPayload(payload any, override *runtime.PublicEncryptionKey) (*runtime.EncryptedPayloadEnvelope, error) {
	key, err := c.resolvePublicEncryptionKey(override)
	if err != nil {
		return nil, err
	}

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal encrypted payload: %w", err)
	}

	normalizedAlgorithm := normalizeAlgorithm(key.Algorithm)
	canonicalAlgorithm := canonicalEncryptionAlgorithm(normalizedAlgorithm)
	if canonicalAlgorithm == "" {
		return nil, fmt.Errorf("unsupported encryption algorithm: %s", key.Algorithm)
	}

	envelope := &runtime.EncryptedPayloadEnvelope{
		Algorithm: canonicalAlgorithm,
		KeyID:     key.KeyID,
	}

	switch normalizedAlgorithm {
	case "NONE":
		// Pass-through payload encoded in base64.
		envelope.Payload = base64.StdEncoding.EncodeToString(plaintext)
	case "RSAOAEP256":
		publicKey, err := parseRSAPublicKey(key.PublicKey)
		if err != nil {
			return nil, err
		}
		encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, plaintext, nil)
		if err != nil {
			return nil, fmt.Errorf("encrypt payload: %w", err)
		}
		envelope.Payload = base64.StdEncoding.EncodeToString(encrypted)
	case "AES256GCMRSAOAEP256":
		encryptedPayload, encryptedKey, iv, err := encryptHybridPayload(plaintext, key.PublicKey)
		if err != nil {
			return nil, err
		}
		envelope.Payload = base64.StdEncoding.EncodeToString(encryptedPayload)
		envelope.EncryptedKey = base64.StdEncoding.EncodeToString(encryptedKey)
		envelope.IV = base64.StdEncoding.EncodeToString(iv)
	default:
		return nil, fmt.Errorf("unsupported encryption algorithm: %s", key.Algorithm)
	}

	return envelope, nil
}

func (c *HTTPClient) resolvePublicEncryptionKey(override *runtime.PublicEncryptionKey) (*runtime.PublicEncryptionKey, error) {
	if override != nil {
		if err := validatePublicEncryptionKey(override); err != nil {
			return nil, err
		}
		return override, nil
	}
	if c.config.Encryption != nil && c.config.Encryption.PublicEncryptionKey != nil {
		if err := validatePublicEncryptionKey(c.config.Encryption.PublicEncryptionKey); err != nil {
			return nil, err
		}
		return c.config.Encryption.PublicEncryptionKey, nil
	}
	return nil, fmt.Errorf("no public encryption key configured")
}

func validatePublicEncryptionKey(key *runtime.PublicEncryptionKey) error {
	if key == nil {
		return fmt.Errorf("public encryption key is required")
	}
	if strings.TrimSpace(key.PublicKey) == "" {
		return fmt.Errorf("public encryption key value is required")
	}
	if strings.TrimSpace(key.Algorithm) == "" {
		return fmt.Errorf("public encryption key algorithm is required")
	}
	if strings.TrimSpace(key.KeyID) == "" {
		return fmt.Errorf("public encryption key keyId is required")
	}
	return nil
}

var nonAlnumRegex = regexp.MustCompile(`[^A-Z0-9]`)

func normalizeAlgorithm(algorithm string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(algorithm))
	return nonAlnumRegex.ReplaceAllString(cleaned, "")
}

func canonicalEncryptionAlgorithm(normalizedAlgorithm string) string {
	switch normalizedAlgorithm {
	case "NONE":
		return "NONE"
	case "RSAOAEP256":
		return "RSA_OAEP_256"
	case "AES256GCMRSAOAEP256":
		return "AES_256_GCM_RSA_OAEP_256"
	default:
		return ""
	}
}

func encryptHybridPayload(plaintext []byte, publicKeyPEM string) ([]byte, []byte, []byte, error) {
	publicKey, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return nil, nil, nil, err
	}

	aesKey := make([]byte, 32)
	defer clear(aesKey)
	if _, err := rand.Read(aesKey); err != nil {
		return nil, nil, nil, fmt.Errorf("generate AES key: %w", err)
	}

	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return nil, nil, nil, fmt.Errorf("generate AES-GCM iv: %w", err)
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create AES-GCM cipher: %w", err)
	}
	encryptedPayload := gcm.Seal(nil, iv, plaintext, nil)

	wrappedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, aesKey, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encrypt AES key: %w", err)
	}

	return encryptedPayload, wrappedKey, iv, nil
}

func parseRSAPublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not RSA")
	}
	return rsaPub, nil
}

func decodeError(statusCode int, body []byte, headers http.Header) error {
	message := defaultErrorMessage(statusCode)
	code := ""
	var details map[string]any
	var payload map[string]any
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil {
		// Generated servers answer with an RFC 9457 problem whose user-safe
		// message is `detail`; `error` and `message` are older body shapes.
		if parsedMessage := parsePayloadString(payload, "detail"); parsedMessage != "" {
			message = parsedMessage
		} else if parsedMessage := parsePayloadString(payload, "error"); parsedMessage != "" {
			message = parsedMessage
		} else if parsedMessage := parsePayloadString(payload, "message"); parsedMessage != "" {
			message = parsedMessage
		}
		code = parsePayloadString(payload, "code")
		details = parsePayloadMap(payload, "details")
	}

	apiErr := &APIError{
		Message:          message,
		StatusCode:       statusCode,
		Body:             body,
		Code:             code,
		Details:          details,
		ValidationErrors: parseValidationErrors(body),
		RequestID:        strings.TrimSpace(headers.Get("X-Request-ID")),
		ResponseHeaders:  cloneHeaders(headers),
	}

	if statusCode == http.StatusUpgradeRequired && code == "client_too_old" {
		detail := func(key string) string {
			text, _ := details[key].(string)
			return text
		}
		return &ClientTooOldError{APIError: apiErr, Client: detail("client"), Version: detail("version"), Minimum: detail("minimum")}
	}

	switch statusCode {
	case http.StatusUnauthorized:
		return &AuthenticationError{APIError: apiErr}
	case http.StatusForbidden:
		return &AuthorizationError{APIError: apiErr}
	case http.StatusTooManyRequests:
		return &RateLimitError{
			APIError:   apiErr,
			RetryAfter: parseRetryAfter(headers),
		}
	default:
		return apiErr
	}
}

// problemCode returns the code of an error body: code of an RFC 9457
// problem, or error.code of the legacy {"error": {...}} envelope.
func problemCode(body []byte) string {
	var payload map[string]any
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return ""
	}
	if code := parsePayloadString(payload, "code"); code != "" {
		return code
	}
	if envelope := parsePayloadMap(payload, "error"); envelope != nil {
		return parsePayloadString(envelope, "code")
	}
	return ""
}

func defaultErrorMessage(statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized:
		return "authentication required"
	case http.StatusForbidden:
		return "access forbidden"
	case http.StatusTooManyRequests:
		return "rate limit exceeded"
	default:
		return "api request failed"
	}
}

func parsePayloadString(payload map[string]any, key string) string {
	rawValue, ok := payload[key]
	if !ok {
		return ""
	}
	value, ok := rawValue.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// parseValidationErrors reads the `errors` member of a problem body into the
// generated ValidationErrors type. It returns nil when the body has no field
// errors. A filter refusal is an example: its `errors` member is a list.
func parseValidationErrors(body []byte) types.ValidationErrors {
	var problem struct {
		Errors json.RawMessage `json:"errors"`
	}
	if len(body) == 0 || json.Unmarshal(body, &problem) != nil {
		return nil
	}
	return decodeValidationErrors(problem.Errors)
}

// decodeValidationErrors decodes one level of field errors. A list under a
// field name holds that field's failures. An object holds the errors of a
// nested field. A value of another shape is not a field error and is
// skipped.
func decodeValidationErrors(raw json.RawMessage) types.ValidationErrors {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	validationErrors := types.NewValidationErrors()
	for field, value := range fields {
		var fieldErrors []types.ValidationError
		if json.Unmarshal(value, &fieldErrors) == nil {
			validationErrors.SetFieldErrors(field, fieldErrors)
		} else if nested := decodeValidationErrors(value); nested.HasErrors() {
			validationErrors.AddNestedError(field, nested)
		}
	}
	if !validationErrors.HasErrors() {
		return nil
	}
	return validationErrors
}

func parsePayloadMap(payload map[string]any, key string) map[string]any {
	rawValue, ok := payload[key]
	if !ok {
		return nil
	}
	value, ok := rawValue.(map[string]any)
	if !ok {
		return nil
	}
	return value
}

func parseRetryAfter(headers http.Header) *int {
	if headers == nil {
		return nil
	}

	retryAfterValue := strings.TrimSpace(headers.Get("Retry-After"))
	if retryAfterValue == "" {
		return nil
	}

	if seconds, err := strconv.Atoi(retryAfterValue); err == nil && seconds >= 0 {
		return &seconds
	}

	retryAfterTime, err := http.ParseTime(retryAfterValue)
	if err != nil {
		return nil
	}

	secondsUntilRetry := int(time.Until(retryAfterTime).Seconds())
	if secondsUntilRetry < 0 {
		secondsUntilRetry = 0
	}
	return &secondsUntilRetry
}

var safeResponseHeaders = map[string]struct{}{
	"Content-Type":          {},
	"Retry-After":           {},
	"X-Request-Id":          {},
	"X-Correlation-Id":      {},
	"X-Trace-Id":            {},
	"X-Ratelimit-Limit":     {},
	"X-Ratelimit-Remaining": {},
	"X-Ratelimit-Reset":     {},
}

func cloneHeaders(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	safe := make(http.Header, len(safeResponseHeaders))
	for allowed := range safeResponseHeaders {
		if vals := headers.Values(allowed); len(vals) > 0 {
			safe[http.CanonicalHeaderKey(allowed)] = append([]string(nil), vals...)
		}
	}
	if len(safe) == 0 {
		return nil
	}
	return safe
}
