package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSigV4 signs the requests of AWS's Signature Version 4 examples for
// S3 and checks the signatures they document.
func TestSigV4(t *testing.T) {
	const (
		id     = "AKIAIOSFODNN7EXAMPLE"
		secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	)
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, url, rangeHeader, want string
	}{
		{"GET Object", "https://examplebucket.s3.amazonaws.com/test.txt", "bytes=0-9", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"GET Bucket", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", "", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	} {
		req, err := http.NewRequest(http.MethodGet, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.rangeHeader != "" {
			req.Header.Set("Range", tc.rangeHeader)
		}
		signV4(req, nil, id, secret, "us-east-1", at)
		got := req.Header.Get("Authorization")
		if !strings.HasSuffix(got, "Signature="+tc.want) || !strings.HasPrefix(got, "AWS4-HMAC-SHA256 Credential="+id+"/20130524/us-east-1/s3/aws4_request, SignedHeaders=") {
			t.Errorf("%s: Authorization = %s, want signature %s", tc.name, got, tc.want)
		}
	}
}

// TestS3Credentials: the access key id is the token's id, and the secret
// access key the hex SHA-256 of its value.
func TestS3Credentials(t *testing.T) {
	id, secret := S3Credentials("tok-id", "tok-value")
	sum := sha256.Sum256([]byte("tok-value"))
	if id != "tok-id" || secret != hex.EncodeToString(sum[:]) {
		t.Errorf("S3Credentials = %s, %s", id, secret)
	}
}

// fakeAPI is Cloudflare's REST API and R2's S3 API in one test server: it
// answers the calls restCloud makes, refuses every other, and checks the
// bearer token and each S3 request's signature.
type fakeAPI struct {
	t     *testing.T
	token string

	mu sync.Mutex
	// userToken has the account's endpoint refuse the token.
	userToken bool
	subdomain string
	buckets   map[string]bool
	stores    []SecretsStore
	secrets   map[string]map[string]Secret // store, name
	values    map[string]string            // secret id
	objects   map[string][]byte
	meta      map[string]http.Header
	calls     []string
	// hosts are the hosts the S3 calls named, in order.
	hosts []string
}

func (f *fakeAPI) reply(w http.ResponseWriter, status int, result any, info map[string]int) {
	body := map[string]any{"success": status < 300, "errors": []any{}, "result": result}
	if status >= 300 {
		body["errors"] = []map[string]any{{"code": 1000 + status, "message": http.StatusText(status)}}
	}
	if info != nil {
		body["result_info"] = info
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if strings.HasPrefix(r.URL.Path, "/client/v4/") {
		f.rest(w, r)
		return
	}
	f.s3(w, r)
}

func (f *fakeAPI) rest(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.reply(w, http.StatusUnauthorized, nil, nil)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	acct := "/accounts/" + accountOf
	var body json.RawMessage
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		body = data
	}
	switch {
	case r.Method == http.MethodGet && path == acct+"/tokens/verify":
		if f.userToken {
			f.reply(w, http.StatusUnauthorized, nil, nil)
			return
		}
		f.reply(w, http.StatusOK, map[string]string{"id": "tok-id", "status": "active"}, nil)
	case r.Method == http.MethodGet && path == "/user/tokens/verify":
		f.reply(w, http.StatusOK, map[string]string{"id": "user-tok-id", "status": "active"}, nil)
	case r.Method == http.MethodGet && path == acct+"/workers/subdomain":
		if f.subdomain == "" {
			f.reply(w, http.StatusNotFound, nil, nil)
			return
		}
		f.reply(w, http.StatusOK, map[string]string{"subdomain": f.subdomain}, nil)
	case r.Method == http.MethodGet && path == acct+"/tokens/permission_groups":
		// Two pages of one group each.
		page := r.URL.Query().Get("page")
		f.reply(w, http.StatusOK, []PermissionGroup{{ID: "g" + page, Name: "Group " + page}}, map[string]int{"page": atoi(page), "total_pages": 2})
	case r.Method == http.MethodGet && path == acct+"/secrets_store/stores":
		f.reply(w, http.StatusOK, f.stores, map[string]int{"page": 1, "total_pages": 1})
	case r.Method == http.MethodPost && path == acct+"/secrets_store/stores":
		var req struct{ Name string }
		_ = json.Unmarshal(body, &req)
		store := SecretsStore{ID: fmt.Sprintf("%032d", len(f.stores)+1), Name: req.Name}
		f.stores = append(f.stores, store)
		f.reply(w, http.StatusOK, store, nil)
	case strings.HasPrefix(path, acct+"/secrets_store/stores/"):
		f.secretsCall(w, r, strings.TrimPrefix(path, acct+"/secrets_store/stores/"), body)
	case r.Method == http.MethodGet && strings.HasPrefix(path, acct+"/r2/buckets/"):
		if !f.buckets[strings.TrimPrefix(path, acct+"/r2/buckets/")] {
			f.reply(w, http.StatusNotFound, nil, nil)
			return
		}
		f.reply(w, http.StatusOK, map[string]string{}, nil)
	case r.Method == http.MethodPost && path == acct+"/r2/buckets":
		var req struct{ Name string }
		_ = json.Unmarshal(body, &req)
		f.buckets[req.Name] = true
		f.reply(w, http.StatusOK, map[string]string{"name": req.Name}, nil)
	default:
		f.t.Errorf("unexpected call %s %s", r.Method, r.URL)
		f.reply(w, http.StatusNotFound, nil, nil)
	}
}

func (f *fakeAPI) secretsCall(w http.ResponseWriter, r *http.Request, rest string, body json.RawMessage) {
	store, tail, _ := strings.Cut(rest, "/secrets")
	secrets := f.secrets[store]
	if secrets == nil {
		secrets = map[string]Secret{}
		f.secrets[store] = secrets
	}
	switch {
	case r.Method == http.MethodGet && tail == "":
		search := r.URL.Query().Get("search")
		var out []Secret
		for name, s := range secrets {
			if strings.Contains(name, search) {
				out = append(out, s)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		f.reply(w, http.StatusOK, out, map[string]int{"page": 1, "total_pages": 1})
	case r.Method == http.MethodPost && tail == "":
		var req []struct {
			Name, Value, Comment string
			Scopes               []string
		}
		if err := json.Unmarshal(body, &req); err != nil || len(req) != 1 || !slices.Equal(req[0].Scopes, []string{"workers"}) {
			f.t.Errorf("create secret: %s", body)
		}
		s := Secret{ID: fmt.Sprintf("s%d", len(f.values)+1), Name: req[0].Name, Status: "active"}
		secrets[s.Name] = s
		f.values[s.ID] = req[0].Value
		f.reply(w, http.StatusOK, []Secret{s}, nil)
	case r.Method == http.MethodPatch && strings.HasPrefix(tail, "/"):
		var req struct{ Value string }
		_ = json.Unmarshal(body, &req)
		f.values[strings.TrimPrefix(tail, "/")] = req.Value
		f.reply(w, http.StatusOK, map[string]string{}, nil)
	default:
		f.t.Errorf("unexpected call %s %s", r.Method, r.URL)
		f.reply(w, http.StatusNotFound, nil, nil)
	}
}

// s3 answers R2's S3 calls on path-style URLs, after it checks the
// signature with the token's S3 credentials.
func (f *fakeAPI) s3(w http.ResponseWriter, r *http.Request) {
	f.hosts = append(f.hosts, r.Host)
	body, _ := io.ReadAll(r.Body)
	if err := f.checkSignature(r, body); err != nil {
		f.t.Errorf("%s %s: %v", r.Method, r.URL, err)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !f.buckets[bucket] {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<Error><Code>NoSuchBucket</Code><Message>no bucket</Message></Error>")
		return
	}
	switch {
	case r.Method == http.MethodGet && key == "":
		var keys []string
		for k := range f.objects {
			if b, k, _ := strings.Cut(k, "/"); b == bucket && strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		// One key a page, to walk the continuation.
		start := 0
		if token := r.URL.Query().Get("continuation-token"); token != "" {
			start = atoi(token)
		}
		var b strings.Builder
		b.WriteString("<ListBucketResult>")
		if start < len(keys) {
			fmt.Fprintf(&b, "<Contents><Key>%s</Key></Contents>", keys[start])
		}
		if start+1 < len(keys) {
			fmt.Fprintf(&b, "<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>", start+1)
		}
		b.WriteString("</ListBucketResult>")
		_, _ = io.WriteString(w, b.String())
	case r.Method == http.MethodGet:
		data, ok := f.objects[bucket+"/"+key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>no key</Message></Error>")
			return
		}
		for k, v := range f.meta[bucket+"/"+key] {
			w.Header()[k] = v
		}
		_, _ = w.Write(data)
	case r.Method == http.MethodPut:
		if _, ok := f.objects[bucket+"/"+key]; ok && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code><Message>At least one of the pre-conditions you specified did not hold</Message></Error>")
			return
		}
		f.objects[bucket+"/"+key] = body
		meta := http.Header{}
		for k, v := range r.Header {
			if strings.HasPrefix(k, "X-Amz-Meta-") {
				meta[k] = v
			}
		}
		f.meta[bucket+"/"+key] = meta
	case r.Method == http.MethodDelete:
		delete(f.objects, bucket+"/"+key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// checkSignature signs a copy of the request again, as the server sees it,
// and compares: the key id, the scope, the payload's hash and the
// signature.
func (f *fakeAPI) checkSignature(r *http.Request, body []byte) error {
	auth := r.Header.Get("Authorization")
	stamp, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		return errors.New("the payload hash does not match the body")
	}
	u := *r.URL
	u.Scheme, u.Host = "http", r.Host
	again, err := http.NewRequest(r.Method, u.String(), nil)
	if err != nil {
		return err
	}
	for k, v := range r.Header {
		if k != "Authorization" {
			again.Header[k] = v
		}
	}
	id, secret := S3Credentials("tok-id", f.token)
	signV4(again, body, id, secret, "auto", stamp)
	if want := again.Header.Get("Authorization"); auth != want {
		return fmt.Errorf("Authorization %s, want %s", auth, want)
	}
	return nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// accountOf is the fixture's account.
const accountOf = "0123456789abcdef0123456789abcdef"

// newTestCloud is restCloud against a fakeAPI.
func newTestCloud(t *testing.T) (*restCloud, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{
		t: t, token: "tok-value", subdomain: "acme",
		buckets: map[string]bool{}, secrets: map[string]map[string]Secret{}, values: map[string]string{},
		objects: map[string][]byte{}, meta: map[string]http.Header{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	c := NewCloud("tok-value").(*restCloud)
	c.api = srv.URL + "/client/v4"
	c.r2 = func(string, string) string { return srv.URL }
	return c, api
}

// TestCloudREST covers the REST calls: the token's verification on the
// account's endpoint and the user's, the workers.dev subdomain or none,
// every page of a list, the Secrets Store and its secrets, and a bucket
// created once.
func TestCloudREST(t *testing.T) {
	c, api := newTestCloud(t)
	ctx := context.Background()
	tok, err := c.VerifyToken(ctx, accountOf)
	if err != nil || tok != (Token{ID: "tok-id", Account: true}) {
		t.Errorf("VerifyToken = %+v, %v", tok, err)
	}
	api.userToken = true
	if tok, err := c.VerifyToken(ctx, accountOf); err != nil || tok != (Token{ID: "user-tok-id"}) {
		t.Errorf("VerifyToken of a user token = %+v, %v", tok, err)
	}
	api.userToken = false
	bad := NewCloud("another").(*restCloud)
	bad.api = c.api
	if _, err := bad.VerifyToken(ctx, accountOf); err == nil || !strings.Contains(err.Error(), "is neither an account token") {
		t.Errorf("VerifyToken of a refused token: %v", err)
	}

	if sub, err := c.WorkersSubdomain(ctx, accountOf); sub != "acme" || err != nil {
		t.Errorf("WorkersSubdomain = %q, %v", sub, err)
	}
	api.subdomain = ""
	if sub, err := c.WorkersSubdomain(ctx, accountOf); sub != "" || err != nil {
		t.Errorf("WorkersSubdomain of an account with none = %q, %v", sub, err)
	}
	groups, err := c.PermissionGroups(ctx, accountOf)
	if err != nil || len(groups) != 2 || groups[0].ID != "g1" || groups[1].ID != "g2" {
		t.Errorf("PermissionGroups = %+v, %v", groups, err)
	}

	id, err := c.CreateSecretsStore(ctx, accountOf, "default_secrets_store")
	if err != nil {
		t.Fatal(err)
	}
	if stores, err := c.SecretsStores(ctx, accountOf); err != nil || len(stores) != 1 || stores[0].ID != id {
		t.Errorf("SecretsStores = %+v, %v", stores, err)
	}
	if err := c.CreateSecret(ctx, accountOf, id, "s-Staging-A-B", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	secrets, err := c.Secrets(ctx, accountOf, id, "s-Staging")
	if err != nil || len(secrets) != 1 || secrets[0].Name != "s-Staging-A-B" {
		t.Fatalf("Secrets = %+v, %v", secrets, err)
	}
	if err := c.UpdateSecret(ctx, accountOf, id, secrets[0].ID, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if api.values[secrets[0].ID] != "v2" {
		t.Errorf("the secret holds %q", api.values[secrets[0].ID])
	}

	for i, want := range []bool{true, false} {
		created, err := c.EnsureBucket(ctx, accountOf, "superschematic-state")
		if err != nil || created != want {
			t.Errorf("EnsureBucket %d = %v, %v", i, created, err)
		}
	}
	var apiErr *APIError
	if _, err := c.call(ctx, http.MethodGet, "/accounts/"+accountOf+"/r2/buckets/missing", nil, nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || !strings.Contains(err.Error(), "Not Found (code 1404)") {
		t.Errorf("a missing bucket: %v", err)
	}
}

// TestCloudS3 covers the object calls over R2's S3 API, path-style and
// signed with the token's S3 credentials: a put with metadata, a get that
// returns it, a missing object, a list across pages, a delete of a present
// and of a missing object, and a conditional write that finds an object.
func TestCloudS3(t *testing.T) {
	c, api := newTestCloud(t)
	ctx := context.Background()
	api.buckets["superschematic-state"] = true
	if _, _, err := c.GetObject(ctx, accountOf, "superschematic-state", "nothing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("GetObject of a missing object: %v", err)
	}
	for _, key := range []string{"superschematic/bundles/s/api/ab/main.js", "superschematic/bundles/s/api/ab/a b+c.wasm", "superschematic/other"} {
		if err := c.PutObject(ctx, accountOf, "superschematic-state", key, []byte("data of "+key), map[string]string{"mode": "644"}); err != nil {
			t.Fatal(err)
		}
	}
	data, meta, err := c.GetObject(ctx, accountOf, "superschematic-state", "superschematic/bundles/s/api/ab/a b+c.wasm")
	if err != nil || string(data) != "data of superschematic/bundles/s/api/ab/a b+c.wasm" || meta["mode"] != "644" {
		t.Errorf("GetObject = %q, %v, %v", data, meta, err)
	}
	keys, err := c.ListObjects(ctx, accountOf, "superschematic-state", "superschematic/bundles/")
	if want := []string{"superschematic/bundles/s/api/ab/a b+c.wasm", "superschematic/bundles/s/api/ab/main.js"}; err != nil || !slices.Equal(keys, want) {
		t.Errorf("ListObjects = %v, %v; want %v", keys, err, want)
	}
	for range 2 {
		if err := c.DeleteObject(ctx, accountOf, "superschematic-state", "superschematic/other"); err != nil {
			t.Errorf("DeleteObject: %v", err)
		}
	}
	// A conditional write puts the first object at a key, signed with its
	// If-None-Match header, and puts nothing the second time.
	for i, want := range []bool{true, false} {
		created, err := c.CreateObject(ctx, accountOf, "superschematic-state", "superschematic/passphrase-check", []byte(fmt.Sprintf("write %d", i)))
		if err != nil || created != want {
			t.Errorf("CreateObject %d = %v, %v; want %v", i, created, err, want)
		}
	}
	if data, _, err := c.GetObject(ctx, accountOf, "superschematic-state", "superschematic/passphrase-check"); err != nil || string(data) != "write 0" {
		t.Errorf("after two conditional writes the object holds %q, %v", data, err)
	}
	if _, _, err := c.GetObject(ctx, accountOf, "missing-bucket", "x"); err == nil || !strings.Contains(err.Error(), "NoSuchBucket") {
		t.Errorf("GetObject of a missing bucket: %v", err)
	}
	// The first S3 call verified the token, for its id; the rest reuse it.
	var verifies int
	for _, call := range api.calls {
		if strings.HasSuffix(call, "/tokens/verify") {
			verifies++
		}
	}
	if verifies != 1 {
		t.Errorf("the S3 calls verified the token %d times", verifies)
	}
}

// TestCloudS3InAJurisdiction (D64, amended): the object calls of
// InJurisdiction's Cloud go to the jurisdiction's S3 host over TLS,
// `<account>.eu.r2.cloudflarestorage.com`, signed for that host, which the
// stand-in signs again from the Host it received; the Cloud they came from
// keeps the default host.
func TestCloudS3InAJurisdiction(t *testing.T) {
	api := &fakeAPI{
		t: t, token: "tok-value", subdomain: "acme",
		buckets: map[string]bool{"shop-media": true}, secrets: map[string]map[string]Secret{}, values: map[string]string{},
		objects: map[string][]byte{}, meta: map[string]http.Header{},
	}
	srv := httptest.NewTLSServer(api)
	t.Cleanup(srv.Close)
	// Every host the Cloud dials reaches the stand-in, so the requests
	// carry R2's real host names.
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	c := NewCloud("tok-value").(*restCloud)
	c.api = srv.URL + "/client/v4"
	c.client = &http.Client{Transport: transport}
	ctx := context.Background()
	eu := c.InJurisdiction("eu")
	if err := eu.PutObject(ctx, accountOf, "shop-media", "carts/ada.png", []byte("ada"), nil); err != nil {
		t.Fatal(err)
	}
	keys, err := eu.ListObjects(ctx, accountOf, "shop-media", "carts/")
	if err != nil || !slices.Equal(keys, []string{"carts/ada.png"}) {
		t.Errorf("ListObjects = %v, %v", keys, err)
	}
	if err := c.DeleteObject(ctx, accountOf, "shop-media", "carts/ada.png"); err != nil {
		t.Fatal(err)
	}
	want := []string{accountOf + ".eu.r2.cloudflarestorage.com", accountOf + ".eu.r2.cloudflarestorage.com", accountOf + ".r2.cloudflarestorage.com"}
	if !slices.Equal(api.hosts, want) {
		t.Errorf("the S3 calls went to %v, want %v", api.hosts, want)
	}
	if cloud, err := cloudIn(c, ""); err != nil || cloud != Cloud(c) {
		t.Errorf("cloudIn of the default jurisdiction = %v, %v", cloud, err)
	}
}
