package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cloud is what the cloudflare target calls on Cloudflare directly,
// outside the provisioner (section 18.3): what bootstrap makes before
// Pulumi can run, the secrets `stack secrets set` writes, and the objects
// the state store and the deploy keep in R2. NewCloud returns the one over
// Cloudflare's REST API and R2's S3 API, with an API token; tests pass a
// fake. Every call is idempotent but CreateSecretsStore and CreateSecret,
// which their callers make only after they found none, and CreateObject,
// which writes only the first time.
type Cloud interface {
	// VerifyToken checks the token the Cloud calls with: an account token
	// of account, else a user token. It returns the token's id, which is
	// also its S3 access key id, and refuses a token that is not active.
	VerifyToken(ctx context.Context, account string) (Token, error)

	// WorkersSubdomain returns the account's workers.dev subdomain, or ""
	// when the account has none yet.
	WorkersSubdomain(ctx context.Context, account string) (string, error)

	// PermissionGroups returns the permission groups an account token of
	// the account may hold.
	PermissionGroups(ctx context.Context, account string) ([]PermissionGroup, error)

	// SecretsStores returns the account's Secrets Stores, and
	// CreateSecretsStore creates one and returns its id.
	SecretsStores(ctx context.Context, account string) ([]SecretsStore, error)
	CreateSecretsStore(ctx context.Context, account, name string) (string, error)

	// Secrets returns the secrets of a store whose name or comment holds
	// search, every one when it is empty: their names and ids, never a
	// value.
	Secrets(ctx context.Context, account, store, search string) ([]Secret, error)

	// CreateSecret creates a secret holding value that Workers may read,
	// and UpdateSecret replaces the value of the secret id names.
	CreateSecret(ctx context.Context, account, store, name string, value []byte) error
	UpdateSecret(ctx context.Context, account, store, id string, value []byte) error

	// EnsureBucket creates an R2 bucket, unless it exists.
	EnsureBucket(ctx context.Context, account, bucket string) (created bool, err error)

	// GetObject returns an object's content and its metadata, or an error
	// that wraps fs.ErrNotExist.
	GetObject(ctx context.Context, account, bucket, key string) ([]byte, map[string]string, error)

	// PutObject replaces an object, with metadata, which may be nil.
	PutObject(ctx context.Context, account, bucket, key string, data []byte, metadata map[string]string) error

	// CreateObject writes an object only if the key holds none, a PUT
	// with `If-None-Match: *`: created is false, with no error, when one
	// was there, which a write at the same time may have put.
	CreateObject(ctx context.Context, account, bucket, key string, data []byte) (created bool, err error)

	// DeleteObject deletes an object; a missing one is not an error.
	DeleteObject(ctx context.Context, account, bucket, key string) error

	// ListObjects returns the keys of the objects under prefix, sorted.
	ListObjects(ctx context.Context, account, bucket, prefix string) ([]string, error)

	// CreateWorkflowInstance starts an instance of a Workflow with params
	// as its event's payload, and returns the instance's id. A Workflow
	// that does not exist is an error that wraps fs.ErrNotExist.
	CreateWorkflowInstance(ctx context.Context, account, workflow string, params any) (string, error)

	// WorkflowInstance returns an instance's status and, once it has
	// failed, its error.
	WorkflowInstance(ctx context.Context, account, workflow, id string) (*WorkflowInstance, error)

	// TerminateWorkflowInstance stops an instance; one that has ended
	// already is not an error.
	TerminateWorkflowInstance(ctx context.Context, account, workflow, id string) error
}

// WorkflowInstance is a Workflow instance as Cloudflare reports it.
type WorkflowInstance struct {
	// Status is queued, running, paused, waiting, waitingForPause,
	// errored, terminated or complete, among others.
	Status string `json:"status"`

	// Error is what the instance failed with: the message and the name of
	// the error its run threw.
	Error *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Done reports whether the instance has ended: complete, errored or
// terminated.
func (w *WorkflowInstance) Done() bool {
	switch w.Status {
	case WorkflowComplete, WorkflowErrored, WorkflowTerminated:
		return true
	}
	return false
}

// The statuses of a Workflow instance that has ended.
const (
	WorkflowComplete   = "complete"
	WorkflowErrored    = "errored"
	WorkflowTerminated = "terminated"
)

// Token is an API token as Cloudflare verifies it.
type Token struct {
	// ID is the token's id: its S3 access key id, and the subject of the
	// tokens bootstrap's graph makes.
	ID string

	// Account is true for an account token, false for a user token.
	Account bool
}

// PermissionGroup is a permission an API token may hold: its id, which a
// token's policy names, its name (`Workers Scripts Write`) and the scopes
// it applies to (`com.cloudflare.api.account`).
type PermissionGroup struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes,omitempty"`
}

// SecretsStore is an account's Secrets Store.
type SecretsStore struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Secret is a secret of a Secrets Store, without its value.
type Secret struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
}

// APIError is Cloudflare's refusal of a call: the HTTP status and the
// errors the response names.
type APIError struct {
	Method, Path string
	Status       int
	Errors       []APIMessage
}

// APIMessage is one error of a refused call.
type APIMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	var parts []string
	for _, m := range e.Errors {
		parts = append(parts, fmt.Sprintf("%s (code %d)", m.Message, m.Code))
	}
	if len(parts) == 0 {
		parts = append(parts, http.StatusText(e.Status))
	}
	return fmt.Sprintf("cloudflare: %s %s: %d: %s", e.Method, e.Path, e.Status, strings.Join(parts, "; "))
}

// NewCloud returns the Cloud over Cloudflare's REST API and R2's S3 API,
// which calls with token. The S3 API signs with the token's id and the
// SHA-256 of its value (S3Credentials), so its first call verifies the
// token.
func NewCloud(token string) Cloud {
	return &restCloud{
		token:  token,
		api:    "https://api.cloudflare.com/client/v4",
		r2:     R2JurisdictionEndpoint,
		client: &http.Client{Timeout: 2 * time.Minute},
		now:    time.Now,
		ids:    map[string]string{},
	}
}

// restCloud is Cloud over the REST and S3 APIs.
type restCloud struct {
	token  string
	api    string
	r2     func(account, jurisdiction string) string
	client *http.Client
	now    func() time.Time

	// jurisdiction is the R2 jurisdiction whose S3 host the object calls
	// reach, "" for the default one (InJurisdiction).
	jurisdiction string

	mu  sync.Mutex
	ids map[string]string
}

// JurisdictionCloud is a Cloud whose object calls can reach the buckets of
// an R2 jurisdiction, which R2's S3 API serves at a host of their own
// (R2JurisdictionEndpoint, D64, amended). NewCloud's is one.
type JurisdictionCloud interface {
	Cloud

	// InJurisdiction returns the Cloud whose object calls reach the
	// buckets of jurisdiction, eu or fedramp; "" is the default one.
	InJurisdiction(jurisdiction string) Cloud
}

var _ JurisdictionCloud = (*restCloud)(nil)

// InJurisdiction returns the Cloud whose object calls go to the S3 host of
// jurisdiction's buckets, signed as every object call is.
func (c *restCloud) InJurisdiction(jurisdiction string) Cloud {
	return &restCloud{token: c.token, api: c.api, r2: c.r2, client: c.client, now: c.now, jurisdiction: jurisdiction, ids: map[string]string{}}
}

// cloudIn is cloud's view of jurisdiction's buckets: cloud itself for the
// default one, else through InJurisdiction, which a Cloud that cannot reach
// a jurisdiction does not have.
func cloudIn(cloud Cloud, jurisdiction string) (Cloud, error) {
	if jurisdiction == "" {
		return cloud, nil
	}
	if jc, ok := cloud.(JurisdictionCloud); ok {
		return jc.InJurisdiction(jurisdiction), nil
	}
	return nil, fmt.Errorf("cloudflare: this Cloud reaches no bucket of R2 jurisdiction %s", jurisdiction)
}

// envelope is the REST API's response.
type envelope struct {
	Success    bool            `json:"success"`
	Errors     []APIMessage    `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// call makes one REST call and decodes its result into out, unless out is
// nil. A body that is not nil goes as JSON.
func (c *restCloud) call(ctx context.Context, method, path string, body, out any) (*envelope, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: %w", method, stripQuery(path), err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: %w", method, stripQuery(path), err)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil && resp.StatusCode < 300 {
		return nil, fmt.Errorf("cloudflare: %s %s: the response is not the API's: %w", method, stripQuery(path), err)
	}
	if resp.StatusCode >= 300 || !env.Success {
		return nil, &APIError{Method: method, Path: stripQuery(path), Status: resp.StatusCode, Errors: env.Errors}
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare: %s %s: %w", method, stripQuery(path), err)
		}
	}
	return &env, nil
}

// list reads every page of a list call into a slice.
func list[T any](ctx context.Context, c *restCloud, path string, query url.Values) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "100")
		var items []T
		env, err := c.call(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(items) == 0 {
			return all, nil
		}
	}
}

func stripQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

// notFound reports whether err is the API's 404.
func notFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

func (c *restCloud) VerifyToken(ctx context.Context, account string) (Token, error) {
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	tok := Token{Account: true}
	_, err := c.call(ctx, http.MethodGet, "/accounts/"+url.PathEscape(account)+"/tokens/verify", nil, &result)
	if err != nil {
		// A user token is not the account's: Cloudflare verifies it on the
		// user's endpoint.
		accountErr := err
		tok.Account = false
		if _, err = c.call(ctx, http.MethodGet, "/user/tokens/verify", nil, &result); err != nil {
			return Token{}, fmt.Errorf("cloudflare: the token in %s is neither an account token of %s (%v) nor a user token (%w)", TokenEnv, account, accountErr, err)
		}
	}
	if result.Status != "active" {
		return Token{}, fmt.Errorf("cloudflare: the token in %s is %s, not active", TokenEnv, result.Status)
	}
	if result.ID == "" {
		return Token{}, fmt.Errorf("cloudflare: the token in %s verified with no id", TokenEnv)
	}
	tok.ID = result.ID
	c.mu.Lock()
	c.ids[account] = result.ID
	c.mu.Unlock()
	return tok, nil
}

func (c *restCloud) WorkersSubdomain(ctx context.Context, account string) (string, error) {
	var result struct {
		Subdomain string `json:"subdomain"`
	}
	_, err := c.call(ctx, http.MethodGet, "/accounts/"+url.PathEscape(account)+"/workers/subdomain", nil, &result)
	if notFound(err) {
		return "", nil
	}
	return result.Subdomain, err
}

func (c *restCloud) workflowPath(account, workflow string) string {
	return "/accounts/" + url.PathEscape(account) + "/workflows/" + url.PathEscape(workflow) + "/instances"
}

func (c *restCloud) CreateWorkflowInstance(ctx context.Context, account, workflow string, params any) (string, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	// The API takes the event's payload as JSON within a string.
	var result struct {
		ID string `json:"id"`
	}
	_, err = c.call(ctx, http.MethodPost, c.workflowPath(account, workflow), map[string]string{"params": string(data)}, &result)
	if notFound(err) {
		return "", fmt.Errorf("%w: %w", err, fs.ErrNotExist)
	}
	if err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", fmt.Errorf("cloudflare: an instance of Workflow %s was created with no id", workflow)
	}
	return result.ID, nil
}

func (c *restCloud) WorkflowInstance(ctx context.Context, account, workflow, id string) (*WorkflowInstance, error) {
	var instance WorkflowInstance
	if _, err := c.call(ctx, http.MethodGet, c.workflowPath(account, workflow)+"/"+url.PathEscape(id)+"?simple=true", nil, &instance); err != nil {
		return nil, err
	}
	return &instance, nil
}

func (c *restCloud) TerminateWorkflowInstance(ctx context.Context, account, workflow, id string) error {
	_, err := c.call(ctx, http.MethodPatch, c.workflowPath(account, workflow)+"/"+url.PathEscape(id)+"/status", map[string]string{"status": "terminate"}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		// The instance has ended: nothing runs to stop.
		return nil
	}
	return err
}

func (c *restCloud) PermissionGroups(ctx context.Context, account string) ([]PermissionGroup, error) {
	return list[PermissionGroup](ctx, c, "/accounts/"+url.PathEscape(account)+"/tokens/permission_groups", nil)
}

func (c *restCloud) SecretsStores(ctx context.Context, account string) ([]SecretsStore, error) {
	return list[SecretsStore](ctx, c, "/accounts/"+url.PathEscape(account)+"/secrets_store/stores", nil)
}

func (c *restCloud) CreateSecretsStore(ctx context.Context, account, name string) (string, error) {
	var store SecretsStore
	if _, err := c.call(ctx, http.MethodPost, "/accounts/"+url.PathEscape(account)+"/secrets_store/stores", map[string]string{"name": name}, &store); err != nil {
		return "", err
	}
	if store.ID == "" {
		return "", fmt.Errorf("cloudflare: the Secrets Store %s was created with no id", name)
	}
	return store.ID, nil
}

func (c *restCloud) secretsPath(account, store string) string {
	return "/accounts/" + url.PathEscape(account) + "/secrets_store/stores/" + url.PathEscape(store) + "/secrets"
}

func (c *restCloud) Secrets(ctx context.Context, account, store, search string) ([]Secret, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	return list[Secret](ctx, c, c.secretsPath(account, store), q)
}

// secretComment says, beside each secret bootstrap's store writes, what
// wrote it.
const secretComment = "written by superschematic stack secrets set"

func (c *restCloud) CreateSecret(ctx context.Context, account, store, name string, value []byte) error {
	body := []map[string]any{{"name": name, "value": string(value), "scopes": []string{"workers"}, "comment": secretComment}}
	_, err := c.call(ctx, http.MethodPost, c.secretsPath(account, store), body, nil)
	return err
}

func (c *restCloud) UpdateSecret(ctx context.Context, account, store, id string, value []byte) error {
	_, err := c.call(ctx, http.MethodPatch, c.secretsPath(account, store)+"/"+url.PathEscape(id), map[string]string{"value": string(value)}, nil)
	return err
}

func (c *restCloud) EnsureBucket(ctx context.Context, account, bucket string) (bool, error) {
	path := "/accounts/" + url.PathEscape(account) + "/r2/buckets"
	_, err := c.call(ctx, http.MethodGet, path+"/"+url.PathEscape(bucket), nil, nil)
	switch {
	case err == nil:
		return false, nil
	case !notFound(err):
		return false, err
	}
	_, err = c.call(ctx, http.MethodPost, path, map[string]string{"name": bucket}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// s3 makes one call to R2's S3 API, signed with the token's S3
// credentials.
func (c *restCloud) s3(ctx context.Context, account, method, bucket, key string, query url.Values, body []byte, header http.Header) (*http.Response, []byte, error) {
	c.mu.Lock()
	id := c.ids[account]
	c.mu.Unlock()
	if id == "" {
		tok, err := c.VerifyToken(ctx, account)
		if err != nil {
			return nil, nil, err
		}
		id = tok.ID
	}
	u, err := url.Parse(c.r2(account, c.jurisdiction))
	if err != nil {
		return nil, nil, err
	}
	u.Path = "/" + bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	if body == nil {
		req.Body = http.NoBody
	}
	req.ContentLength = int64(len(body))
	_, secret := S3Credentials(id, c.token)
	signV4(req, body, id, secret, r2Region, c.now())
	resp, err := c.client.Do(req)
	what := fmt.Sprintf("r2: %s %s/%s", method, bucket, key)
	if err != nil {
		return nil, nil, fmt.Errorf("cloudflare: %s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	if err != nil {
		return nil, nil, fmt.Errorf("cloudflare: %s: %w", what, err)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		_ = xml.Unmarshal(data, &e)
		err := fmt.Errorf("cloudflare: %s: %d %s: %s", what, resp.StatusCode, e.Code, e.Message)
		if resp.StatusCode == http.StatusNotFound {
			err = fmt.Errorf("%w: %w", err, fs.ErrNotExist)
		}
		return resp, nil, err
	}
	return resp, data, nil
}

// r2Region is the region R2's S3 API signs in.
const r2Region = "auto"

// metaPrefix begins each metadata header of an S3 object.
const metaPrefix = "X-Amz-Meta-"

func (c *restCloud) GetObject(ctx context.Context, account, bucket, key string) ([]byte, map[string]string, error) {
	resp, data, err := c.s3(ctx, account, http.MethodGet, bucket, key, nil, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	meta := map[string]string{}
	for k, vs := range resp.Header {
		if name, ok := strings.CutPrefix(http.CanonicalHeaderKey(k), metaPrefix); ok && len(vs) > 0 {
			meta[strings.ToLower(name)] = vs[0]
		}
	}
	return data, meta, nil
}

func (c *restCloud) PutObject(ctx context.Context, account, bucket, key string, data []byte, metadata map[string]string) error {
	header := http.Header{}
	header.Set("Content-Type", "application/octet-stream")
	for k, v := range metadata {
		header.Set(metaPrefix+k, v)
	}
	if data == nil {
		data = []byte{}
	}
	_, _, err := c.s3(ctx, account, http.MethodPut, bucket, key, nil, data, header)
	return err
}

func (c *restCloud) CreateObject(ctx context.Context, account, bucket, key string, data []byte) (bool, error) {
	header := http.Header{}
	header.Set("Content-Type", "application/octet-stream")
	header.Set("If-None-Match", "*")
	if data == nil {
		data = []byte{}
	}
	resp, _, err := c.s3(ctx, account, http.MethodPut, bucket, key, nil, data, header)
	if resp != nil && (resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusConflict) {
		// 412: the key holds an object. 409: another conditional write
		// to the key is in flight, which R2 lets win.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (c *restCloud) DeleteObject(ctx context.Context, account, bucket, key string) error {
	_, _, err := c.s3(ctx, account, http.MethodDelete, bucket, key, nil, nil, nil)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (c *restCloud) ListObjects(ctx context.Context, account, bucket, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		_, data, err := c.s3(ctx, account, http.MethodGet, bucket, "", q, nil, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("cloudflare: r2: list %s/%s: %w", bucket, prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, obj.Key)
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return keys, nil
		}
		token = page.NextContinuationToken
	}
}
