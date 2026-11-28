// fixture-references-api's references and files end to end through the
// generated Go server (#723, D154, D184). TestRowFeaturesGoServerEndToEnd
// writes this file into the generated API module, applies
// fixture-references-db's create.sql in a schema of its own on Postgres,
// and serves the generated routes over the ORM, behind a test
// authenticator, with a bucket in memory whose uploads arrive as a browser
// sends them: a PUT to the URL the route signed, which a server here checks
// against the URL's signature, its expiry and the content type it covers.
// Each call goes through the generated Go SDK.
package fixturereferencesapi_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-references-api"
	orm "example.com/schemas/orm/fixture-references-db"
	sdk "example.com/schemas/sdk/go/fixture-references-api"
	types "example.com/schemas/types/go/fixture-references-api"
	dbtypes "example.com/schemas/types/go/fixture-references-db"
)

func uuidOf(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

var (
	alice = uuidOf(0xa1)
	bob   = uuidOf(0xb2)
	carol = uuidOf(0xc3)
)

// authenticate is the test's authenticator: Authorization: Bearer
// <subject>[:<permission>[,...]] puts that subject on the request, holding
// those permissions; no header, no caller.
func authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			subject, held, _ := strings.Cut(token, ":")
			var permissions []string
			if held != "" {
				permissions = strings.Split(held, ",")
			}
			ctx := session.ContextWithPrincipalID(r.Context(), subject)
			ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "test", Permissions: permissions}})
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// tickets is the project's implementation of getTicket, the one
// operation no generated handler serves.
type tickets struct{ db orm.DatabaseInterface }

func (o tickets) GetTicket(ctx context.Context, id types.IdentityUUID) (*types.Ticket, error) {
	return nil, api.NotFoundError("ticket", orm.ErrNotFound)
}

// bucket is a bucket in memory whose signed URLs a server of its own
// serves: a PUT is stored only when its signature, its expiry and its
// content type hold.
type bucket struct {
	mu      sync.Mutex
	objects map[string][]byte
	server  *httptest.Server
	secret  []byte
}

func newBucket(t *testing.T) *bucket {
	b := &bucket{objects: map[string][]byte{}, secret: []byte("bucket-signing-key")}
	b.server = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.server.Close)
	return b
}

func (b *bucket) signature(method, name, expires, contentType string) string {
	mac := hmac.New(sha256.New, b.secret)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", method, name, expires, contentType)
	return hex.EncodeToString(mac.Sum(nil))
}

func (b *bucket) SignedURL(_ context.Context, name, method string, expires time.Duration, contentType string) (string, error) {
	at := strconv.FormatInt(time.Now().Add(expires).Unix(), 10)
	q := url.Values{"expires": {at}, "signature": {b.signature(method, name, at, contentType)}}
	return b.server.URL + "/reference-files/" + name + "?" + q.Encode(), nil
}

func (b *bucket) serve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/reference-files/")
	q := r.URL.Query()
	at, _ := strconv.ParseInt(q.Get("expires"), 10, 64)
	want := b.signature(r.Method, name, q.Get("expires"), r.Header.Get("Content-Type"))
	if !hmac.Equal([]byte(want), []byte(q.Get("signature"))) || time.Now().Unix() > at {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.objects[name] = body
	b.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (b *bucket) Open(_ context.Context, name string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.objects[name]
	if !ok {
		return nil, rowrefs.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (b *bucket) Delete(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.objects[name]; !ok {
		return rowrefs.ErrObjectNotFound
	}
	delete(b.objects, name)
	return nil
}

func (b *bucket) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// put sends bytes to an upload as a browser would.
func put(t *testing.T, upload types.RowAttachmentUpload, contentType string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(upload.Method, upload.Url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func hexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// openDatabase is the ORM of fixture-references-db over a schema of its
// own, holding the build's create.sql, which it drops when the test ends.
func openDatabase(t *testing.T) *orm.Database {
	t.Helper()
	target := os.Getenv("ROW_FEATURES_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set ROW_FEATURES_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("row_features_go_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(target)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ddl, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := orm.ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// failure is the status and code of a refused call.
type failure struct {
	status int
	code   string
}

func failed(t *testing.T, what string, err error) failure {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	return failure{status: apiErr.StatusCode, code: apiErr.Code}
}

func TestRowFeatures(t *testing.T) {
	db := openDatabase(t)
	files := newBucket(t)
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{
		DB:              db,
		Attachments:     files,
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Ticket: tickets{db: db}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	as := func(token string) *sdk.FixtureReferencesApiSDK {
		t.Helper()
		client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			client.SetToken(token)
		}
		return client
	}
	ctx := context.Background()
	ticket := func(t *testing.T, title string) string {
		t.Helper()
		row, err := db.GetTicketRepository().CreateOne(orm.AsSystem(ctx), &orm.TicketCreate{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return row.Id.String()
	}
	note := func(t *testing.T, title, owner, audience string) string {
		t.Helper()
		ownerID, err := dbtypes.ParseIdentityUUID(owner)
		if err != nil {
			t.Fatal(err)
		}
		row := dbtypes.NewNote()
		row.Title, row.Owner, row.Audience, row.CreatedAt = title, ownerID, audience, dbtypes.TemporalDateTime(time.Now())
		created, err := db.GetNoteRepository().CreateOne(orm.AsSystem(ctx), row)
		if err != nil {
			t.Fatal(err)
		}
		return created.Id.String()
	}
	upload := func(t *testing.T, client *sdk.FixtureReferencesApiSDK, row, name string, body []byte, declared string) types.RowAttachment {
		t.Helper()
		begun, err := client.TicketNamespace.BeginUpload(ctx, row, types.RowAttachmentBeginInput{Name: name, MediaType: "text/plain", Size: types.GenericInt64(len(body)), Sha256: declared})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if begun.Attachment.State != types.RowAttachmentState_Pending {
			t.Fatalf("a begun file is %s", begun.Attachment.State)
		}
		if code := put(t, begun.Upload, begun.Upload.Headers["content-type"], body); code != http.StatusOK {
			t.Fatalf("the upload: %d", code)
		}
		return begun.Attachment
	}
	alices, bobs, carols := as(alice), as(bob), as(carol)
	ref := func(table, key string) types.RowRef { return types.RowRef{Table: table, Key: key} }
	sameKey := func(a, b string) bool {
		x, errX := dbtypes.ParseIdentityUUID(a)
		y, errY := dbtypes.ParseIdentityUUID(b)
		return errX == nil && errY == nil && x == y
	}

	t.Run("a file uploaded straight to the bucket through the signed URL is filed onto a second row, which lists the first in its backlinks", func(t *testing.T) {
		thread := ticket(t, "Quarterly plan")
		other := note(t, "Planning notes", alice, "everyone")
		body := []byte("The plan, in brief.")
		pending := upload(t, alices, thread, "plan.txt", body, hexOf(body))
		file, err := alices.TicketNamespace.CompleteUpload(ctx, thread, pending.Id)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if file.State != types.RowAttachmentState_Ready || file.Size != types.GenericInt64(len(body)) || file.ContentHash != hexOf(body) {
			t.Fatalf("the file is %+v", file)
		}
		objects := files.size()
		filed, err := alices.TicketNamespace.FileAttachment(ctx, thread, file.Id, types.RowAttachmentFileInput{To: ref("Note", other)})
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		if filed.Table != "Note" || !sameKey(filed.Key, other) || filed.ObjectKey != file.ObjectKey || files.size() != objects {
			t.Fatalf("filed %+v, with %d objects, want the note's copy of the same object", filed, files.size())
		}
		listed, err := alices.NoteNamespace.Attachments(ctx, other)
		if err != nil || len(listed) != 1 || listed[0].Id != filed.Id {
			t.Fatalf("the note's files: %+v, %v", listed, err)
		}
		backlinks, err := alices.NoteNamespace.Backlinks(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		if len(backlinks.Items) != 1 || backlinks.Items[0].Table != "Ticket" || !sameKey(backlinks.Items[0].Key, thread) || backlinks.Items[0].Via != types.RowBacklinkVia_Reference || backlinks.Items[0].Role != "filed" || backlinks.Hidden != nil {
			t.Fatalf("the note's backlinks: %+v", backlinks)
		}
		viewed, err := alices.NoteNamespace.Download(ctx, other, filed.Id)
		if err != nil || !strings.Contains(viewed.Url, "/reference-files/"+file.ObjectKey+"?") || viewed.Attachment.Id != filed.Id {
			t.Fatalf("the download: %+v, %v", viewed, err)
		}
	})

	t.Run("a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count", func(t *testing.T) {
		shared := ticket(t, "Shared ticket")
		hidden := note(t, "Bob's private note", bob, "private")
		quiet := note(t, "Alice's private note", alice, "private")
		open := note(t, "Alice's open note", alice, "everyone")
		system := orm.AsSystem(ctx)
		see := "see also"
		for _, in := range []rowrefs.AddInput{
			{From: rowrefs.RowRef{Table: "Note", Key: quiet}, To: rowrefs.RowRef{Table: "Note", Key: hidden}},
			{From: rowrefs.RowRef{Table: "Note", Key: open}, To: rowrefs.RowRef{Table: "Note", Key: hidden}, Role: see},
			{From: rowrefs.RowRef{Table: "Note", Key: open}, To: rowrefs.RowRef{Table: "Ticket", Key: shared}},
			{From: rowrefs.RowRef{Table: "Note", Key: quiet}, To: rowrefs.RowRef{Table: "Ticket", Key: shared}},
		} {
			if _, err := db.References().Add(system, in); err != nil {
				t.Fatal(err)
			}
		}
		refs, err := carols.NoteNamespace.References(ctx, open)
		if err != nil || len(refs) != 1 || refs[0].To.Table != "Ticket" || !sameKey(refs[0].To.Key, shared) {
			t.Fatalf("carol's read of the open note's references: %+v, %v", refs, err)
		}
		seen, err := carols.TicketNamespace.Backlinks(ctx, shared)
		if err != nil || len(seen.Items) != 1 || !sameKey(seen.Items[0].Key, open) || seen.Hidden != nil {
			t.Fatalf("carol's backlinks of the ticket: %+v, %v", seen, err)
		}
		if _, err := carols.NoteNamespace.Backlinks(ctx, hidden); failed(t, "carol's backlinks of bob's note", err).status != 404 {
			t.Fatalf("carol's backlinks of bob's note: %v, want 404", err)
		}
		if _, err := carols.NoteNamespace.AddReference(ctx, open, types.RowReferenceInput{To: ref("Note", hidden)}); failed(t, "carol's reference to bob's note", err).status != 404 {
			t.Fatalf("carol's reference to bob's note: %v, want 404", err)
		}
		owned, err := bobs.NoteNamespace.Backlinks(ctx, hidden)
		if err != nil || len(owned.Items) != 1 || !sameKey(owned.Items[0].Key, open) || owned.Hidden == nil || *owned.Hidden != 1 {
			t.Fatalf("bob's backlinks of his note: %+v, %v, want the open note and one hidden", owned, err)
		}
	})

	t.Run("a reference is added, listed and removed through the routes", func(t *testing.T) {
		from, to := ticket(t, "Login fails"), ticket(t, "Session store")
		role := "blocks"
		added, err := alices.TicketNamespace.AddReference(ctx, from, types.RowReferenceInput{To: ref("Ticket", to), Role: types.InputField[string]{Set: true, Value: role}})
		if err != nil || added.Role != "blocks" || !sameKey(added.To.Key, to) {
			t.Fatalf("add: %+v, %v", added, err)
		}
		listed, err := alices.TicketNamespace.References(ctx, from)
		if err != nil || len(listed) != 1 || listed[0].Id != added.Id {
			t.Fatalf("references: %+v, %v", listed, err)
		}
		removed, err := alices.TicketNamespace.RemoveReference(ctx, from, types.RowReferenceInput{To: ref("Ticket", to)})
		if err != nil || removed.Removed != 1 {
			t.Fatalf("remove: %+v, %v", removed, err)
		}
	})

	t.Run("an upload whose bytes differ is refused, and a file of another row is not found", func(t *testing.T) {
		row := ticket(t, "Bounds")
		wrong := upload(t, alices, row, "a.txt", []byte("other bytes"), hexOf([]byte("declared bytes")))
		_, err := alices.TicketNamespace.CompleteUpload(ctx, row, wrong.Id)
		if got := failed(t, "a complete of other bytes", err); got.status != 422 || got.code != "mismatch" {
			t.Fatalf("a complete of other bytes: %+v, want 422 mismatch", got)
		}
		owner := ticket(t, "Owner")
		body := []byte("owned bytes")
		file, err := alices.TicketNamespace.CompleteUpload(ctx, owner, upload(t, alices, owner, "owned.txt", body, hexOf(body)).Id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := alices.TicketNamespace.Download(ctx, row, file.Id); failed(t, "another row's file", err).status != 404 {
			t.Fatalf("another row's file: %v, want 404", err)
		}
		if ok, err := alices.TicketNamespace.RemoveAttachment(ctx, owner, file.Id); err != nil || !ok {
			t.Fatalf("remove: %v, %v", ok, err)
		}
	})

	t.Run("a permission the set names for an operation replaces the class's", func(t *testing.T) {
		owned := note(t, "Typed files", alice, "everyone")
		body := []byte("note bytes")
		input := types.RowAttachmentBeginInput{Name: "n.txt", MediaType: "text/plain", Size: types.GenericInt64(len(body)), Sha256: hexOf(body)}
		if _, err := bobs.NoteNamespace.BeginUpload(ctx, owned, input); failed(t, "bob's upload", err).status != 403 {
			t.Fatalf("bob's upload: %v, want 403", err)
		}
		if _, err := as(alice+":notes.upload").NoteNamespace.BeginUpload(ctx, owned, input); err != nil {
			t.Fatalf("alice's upload: %v", err)
		}
		// A call with no caller is the server's auth step to refuse, which
		// an API with no authDb leaves to the server that mounts its
		// routes; past it, the anonymous caller reads only what everyone
		// may, and a permission's route refuses it.
		if _, err := as("").NoteNamespace.BeginUpload(ctx, owned, input); failed(t, "an upload with no caller", err).status != 401 {
			t.Fatalf("an upload with no caller: %v, want 401", err)
		}
	})
}
