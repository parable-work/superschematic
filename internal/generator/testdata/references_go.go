// fixture-references-db's Go repositories end to end (D154, D184):
// tickets with a conversation, references and files; notes with an
// audience, references to tickets and notes, and files of a few media
// types. TestGoReferencesEndToEnd writes this file into the generated ORM
// module, applies the generated create.sql in a schema of its own on
// Postgres and runs these cases through the ORM's References and
// Attachments, over a bucket in memory whose uploads arrive as a browser
// sends them: a PUT to the URL Begin signed, which a server here checks
// against the URL's signature, its expiry and the content type it covers
// before it stores the bytes.
package orm_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"

	orm "example.com/schemas/orm/fixture-references-db"
	types "example.com/schemas/types/go/fixture-references-db"
)

func uuidOf(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

var (
	alice = uuidOf(0xa1)
	bob   = uuidOf(0xb2)
	carol = uuidOf(0xc3)
)

// as is ctx acting as the user id: the caller a route binds.
func as(id string) context.Context {
	return orm.WithPrincipal(context.Background(), access.Principal{Subject: id, Kind: access.KindUser})
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
	return b.server.URL + "/records-files/" + name + "?" + q.Encode(), nil
}

func (b *bucket) serve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/records-files/")
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

func (b *bucket) has(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.objects[name]
	return ok
}

func (b *bucket) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// put sends bytes to an upload as a browser would.
func put(t *testing.T, upload rowrefs.Upload, contentType string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(upload.Method, upload.URL, bytes.NewReader(body))
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

func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	target := os.Getenv("REFERENCES_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set REFERENCES_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("references_go_%d", time.Now().UnixNano())
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
	return db, pool
}

func TestReferences(t *testing.T) {
	db, pool := openDatabase(t)
	files := newBucket(t)
	ticket := func(t *testing.T, title string) string {
		t.Helper()
		row, err := db.GetTicketRepository().CreateOne(as(alice), &orm.TicketCreate{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return row.Id.String()
	}
	note := func(t *testing.T, title, owner, audience string) string {
		t.Helper()
		ownerID, err := types.ParseIdentityUUID(owner)
		if err != nil {
			t.Fatal(err)
		}
		row := types.NewNote()
		row.Title, row.Owner, row.Audience, row.CreatedAt = title, ownerID, audience, types.TemporalDateTime(time.Now())
		created, err := db.GetNoteRepository().CreateOne(orm.AsSystem(context.Background()), row)
		if err != nil {
			t.Fatal(err)
		}
		return created.Id.String()
	}
	upload := func(t *testing.T, table, key, name, mediaType string, body []byte, declared string) rowrefs.Attachment {
		t.Helper()
		begun, err := db.Attachments().Begin(as(alice), rowrefs.BeginInput{To: rowrefs.RowRef{Table: table, Key: key}, Name: name, MediaType: mediaType, Size: int64(len(body)), SHA256: declared}, files, 0)
		if err != nil {
			t.Fatal(err)
		}
		if begun.Attachment.State != rowrefs.StatePending {
			t.Fatalf("a begun file is %s", begun.Attachment.State)
		}
		if code := put(t, begun.Upload, begun.Upload.Headers["content-type"], body); code != http.StatusOK {
			t.Fatalf("the upload: %d", code)
		}
		return begun.Attachment
	}
	isNotFound := func(err error) bool { return errors.Is(err, orm.ErrNotFound) }
	ids := func(list []rowrefs.Attachment) []string {
		out := []string{}
		for _, f := range list {
			out = append(out, f.ID)
		}
		return out
	}
	str := func(s string) *string { return &s }

	t.Run("a file posted on one row's thread is filed onto a second row, which lists the first in its backlinks", func(t *testing.T) {
		thread := ticket(t, "Quarterly plan")
		other := note(t, "Planning notes", alice, "everyone")
		body := []byte("The plan, in brief.")
		pending := upload(t, "Ticket", thread, "plan.txt", "text/plain", body, hexOf(body))
		// A signature the bucket did not make is refused.
		begun, err := db.Attachments().Begin(as(alice), rowrefs.BeginInput{To: rowrefs.RowRef{Table: "Ticket", Key: thread}, Name: "x.txt", MediaType: "text/plain", Size: 1, SHA256: hexOf([]byte("x"))}, files, 0)
		if err != nil {
			t.Fatal(err)
		}
		if code := put(t, begun.Upload, "text/html", []byte("x")); code != http.StatusForbidden {
			t.Fatalf("a PUT of another content type: %d", code)
		}
		file, err := db.Attachments().Complete(as(alice), rowrefs.FileRef{Table: "Ticket", ID: pending.ID}, files)
		if err != nil {
			t.Fatal(err)
		}
		if file.State != rowrefs.StateReady || file.Size != int64(len(body)) || file.ContentHash != hexOf(body) || file.Extraction != rowrefs.ProcessingPending {
			t.Fatalf("the completed file: %+v", file)
		}
		// Posted on the ticket's thread as an attachment block.
		block, _ := json.Marshal(rowrefs.BlockOf(file))
		threadID, _ := types.ParseIdentityUUID(thread)
		posted, err := db.GetTicketRepository().Comments().Post(as(alice), threadID, orm.TicketCommentsPostParams{Body: "The plan.", Blocks: []json.RawMessage{block}})
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		postedJSON, _ := json.Marshal(posted)
		_ = json.Unmarshal(postedJSON, &got)
		blocks, _ := got["blocks"].([]any)
		if len(blocks) != 1 || blocks[0].(map[string]any)["uri"] != "attachment:"+file.ID {
			t.Fatalf("the posted message's blocks: %s", postedJSON)
		}
		// Filed onto the note: the same object, no second upload.
		objects := files.size()
		filed, err := db.Attachments().File(as(alice), rowrefs.FileInput{FileRef: rowrefs.FileRef{Table: "Ticket", ID: file.ID}, To: rowrefs.RowRef{Table: "Note", Key: other}})
		if err != nil {
			t.Fatal(err)
		}
		if filed.Table != "Note" || filed.Key != other || filed.ObjectKey != file.ObjectKey || filed.FiledFrom == nil || *filed.FiledFrom != "Ticket:"+thread+":"+file.ID {
			t.Fatalf("the filed file: %+v", filed)
		}
		if files.size() != objects {
			t.Fatal("filing uploaded a second object")
		}
		onNote, err := db.Attachments().List(as(alice), rowrefs.RowRef{Table: "Note", Key: other})
		if err != nil || !slices.Equal(ids(onNote), []string{filed.ID}) {
			t.Fatalf("the note's files: %v, %v", ids(onNote), err)
		}
		onTicket, err := db.Attachments().List(as(alice), rowrefs.RowRef{Table: "Ticket", Key: thread})
		if err != nil || !slices.Equal(ids(onTicket), []string{file.ID}) {
			t.Fatalf("the ticket's files: %v, %v", ids(onTicket), err)
		}
		// The note lists the ticket in its backlinks.
		backlinks, err := db.References().ReferencedBy(as(alice), rowrefs.RowRef{Table: "Note", Key: other}, rowrefs.ReferencedByOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if want := (rowrefs.Backlinks{Items: []rowrefs.Backlink{{Table: "Ticket", Key: thread, Via: rowrefs.ViaReference, Role: str("filed")}}}); !reflect.DeepEqual(backlinks, want) {
			t.Fatalf("the note's backlinks: %+v", backlinks)
		}
		// A download is a signed GET, and asks the file's preview on its
		// first view.
		viewed, err := db.Attachments().Download(as(alice), rowrefs.FileRef{Table: "Note", ID: filed.ID}, files, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(viewed.URL, "/records-files/"+file.ObjectKey+"?") || viewed.Attachment.Preview != rowrefs.ProcessingPending {
			t.Fatalf("the download: %+v", viewed)
		}
		// A processor's work lands on every file that names the object.
		pendingWork, err := db.Attachments().Pending(context.Background(), rowrefs.KindExtraction, 0)
		if err != nil || !slices.Contains(pendingWork, rowrefs.PendingObject{ObjectKey: file.ObjectKey, MediaType: "text/plain", ContentHash: hexOf(body)}) {
			t.Fatalf("the pending extractions: %+v, %v", pendingWork, err)
		}
		n, err := db.Attachments().Processed(context.Background(), rowrefs.ProcessedInput{ObjectKey: file.ObjectKey, Extraction: &rowrefs.ProcessedPart{State: rowrefs.ProcessingDone, Text: str("The plan, in brief.")}})
		if err != nil || n != 2 {
			t.Fatalf("processed %d files: %v", n, err)
		}
		read, err := db.Attachments().Get(as(alice), rowrefs.FileRef{Table: "Note", ID: filed.ID})
		if err != nil || read.ExtractedText == nil || *read.ExtractedText != "The plan, in brief." {
			t.Fatalf("the extracted text: %+v, %v", read, err)
		}
	})

	t.Run("a ref block in a row's thread is a reference of the row, which the target lists", func(t *testing.T) {
		from := ticket(t, "Login fails")
		to := ticket(t, "Session store")
		fromID, _ := types.ParseIdentityUUID(from)
		ref, _ := json.Marshal(map[string]string{"kind": "ref", "schema": "Ticket", "id": to})
		if _, err := db.GetTicketRepository().Comments().Post(as(alice), fromID, orm.TicketCommentsPostParams{Body: "See the session store.", Blocks: []json.RawMessage{ref}}); err != nil {
			t.Fatal(err)
		}
		backlinks, err := db.References().ReferencedBy(as(alice), rowrefs.RowRef{Table: "Ticket", Key: to}, rowrefs.ReferencedByOptions{})
		if err != nil {
			t.Fatal(err)
		}
		one := int64(1)
		if want := []rowrefs.Backlink{{Table: "Ticket", Key: from, Via: rowrefs.ViaComment, Message: &one}}; !reflect.DeepEqual(backlinks.Items, want) || backlinks.Hidden != nil {
			t.Fatalf("the target's backlinks: %+v", backlinks)
		}
	})

	t.Run("a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count", func(t *testing.T) {
		shared := ticket(t, "Shared ticket")
		hidden := note(t, "Bob's private note", bob, "private")
		quiet := note(t, "Alice's private note", alice, "private")
		open := note(t, "Alice's open note", alice, "everyone")
		system := orm.AsSystem(context.Background())
		refs := db.References()
		for _, add := range []rowrefs.AddInput{
			{From: rowrefs.RowRef{Table: "Note", Key: quiet}, To: rowrefs.RowRef{Table: "Note", Key: hidden}},
			{From: rowrefs.RowRef{Table: "Note", Key: open}, To: rowrefs.RowRef{Table: "Note", Key: hidden}, Role: "see also"},
			{From: rowrefs.RowRef{Table: "Note", Key: open}, To: rowrefs.RowRef{Table: "Ticket", Key: shared}},
			{From: rowrefs.RowRef{Table: "Note", Key: quiet}, To: rowrefs.RowRef{Table: "Ticket", Key: shared}},
		} {
			if _, err := refs.Add(system, add); err != nil {
				t.Fatal(err)
			}
		}
		// Carol reads the open note's references: the private note is left
		// out.
		of, err := refs.Of(as(carol), rowrefs.RowRef{Table: "Note", Key: open})
		if err != nil || len(of) != 1 || of[0].To != (rowrefs.RowRef{Table: "Ticket", Key: shared}) {
			t.Fatalf("carol's references of the open note: %+v, %v", of, err)
		}
		// The ticket's backlinks hold the open note alone, and no count.
		backlinks, err := refs.ReferencedBy(as(carol), rowrefs.RowRef{Table: "Ticket", Key: shared}, rowrefs.ReferencedByOptions{})
		if want := (rowrefs.Backlinks{Items: []rowrefs.Backlink{{Table: "Note", Key: open, Via: rowrefs.ViaReference, Role: str("")}}}); err != nil || !reflect.DeepEqual(backlinks, want) {
			t.Fatalf("carol's backlinks of the ticket: %+v, %v", backlinks, err)
		}
		// The private note is not there for her, and neither is a way to
		// point at it.
		if _, err := refs.ReferencedBy(as(carol), rowrefs.RowRef{Table: "Note", Key: hidden}, rowrefs.ReferencedByOptions{}); !isNotFound(err) {
			t.Fatalf("carol's backlinks of the private note: %v", err)
		}
		if _, err := refs.Add(as(carol), rowrefs.AddInput{From: rowrefs.RowRef{Table: "Note", Key: open}, To: rowrefs.RowRef{Table: "Note", Key: hidden}}); !isNotFound(err) {
			t.Fatalf("carol pointing at the private note: %v", err)
		}
		// Bob owns the private note: he sees the open note, and how many he
		// cannot.
		backlinks, err = refs.ReferencedBy(as(bob), rowrefs.RowRef{Table: "Note", Key: hidden}, rowrefs.ReferencedByOptions{})
		oneHidden := 1
		if want := (rowrefs.Backlinks{Items: []rowrefs.Backlink{{Table: "Note", Key: open, Via: rowrefs.ViaReference, Role: str("see also")}}, Hidden: &oneHidden}); err != nil || !reflect.DeepEqual(backlinks, want) {
			t.Fatalf("bob's backlinks of his note: %+v, %v", backlinks, err)
		}
		// Alice sees her own two notes point at the ticket.
		backlinks, err = refs.ReferencedBy(as(alice), rowrefs.RowRef{Table: "Ticket", Key: shared}, rowrefs.ReferencedByOptions{})
		if err != nil {
			t.Fatal(err)
		}
		keys := []string{}
		for _, b := range backlinks.Items {
			keys = append(keys, b.Key)
		}
		sort.Strings(keys)
		want := []string{open, quiet}
		sort.Strings(want)
		if !slices.Equal(keys, want) || backlinks.Hidden != nil {
			t.Fatalf("alice's backlinks of the ticket: %+v", backlinks)
		}
		// A note points at tickets and notes alone; a ticket at any table.
		other := ticket(t, "Elsewhere")
		if added, err := refs.Add(as(alice), rowrefs.AddInput{From: rowrefs.RowRef{Table: "Ticket", Key: other}, To: rowrefs.RowRef{Table: "Ticket", Key: shared}}); err != nil || added.Role != "" {
			t.Fatalf("a ticket pointing at a ticket: %+v, %v", added, err)
		}
	})

	t.Run("a target's delete clears the references to it and tells each row that held one", func(t *testing.T) {
		target := ticket(t, "Short-lived")
		holder := note(t, "Pointing note", alice, "everyone")
		if _, err := db.References().Add(as(alice), rowrefs.AddInput{From: rowrefs.RowRef{Table: "Note", Key: holder}, To: rowrefs.RowRef{Table: "Ticket", Key: target}, Role: "blocks"}); err != nil {
			t.Fatal(err)
		}
		targetID, _ := types.ParseIdentityUUID(target)
		if err := db.GetTicketRepository().DeleteOne(as(alice), targetID); err != nil {
			t.Fatal(err)
		}
		of, err := db.References().Of(as(alice), rowrefs.RowRef{Table: "Note", Key: holder})
		if err != nil || len(of) != 0 {
			t.Fatalf("the holder's references after the delete: %+v, %v", of, err)
		}
		rows, err := pool.Query(context.Background(), `SELECT holder_table, holder_key, target_table, role FROM reference_cleared_queue WHERE target_key = $1`, rowrefs.UUIDToDB(target))
		if err != nil {
			t.Fatal(err)
		}
		var cleared [][4]string
		for rows.Next() {
			var row [4]string
			if err := rows.Scan(&row[0], &row[1], &row[2], &row[3]); err != nil {
				t.Fatal(err)
			}
			cleared = append(cleared, row)
		}
		rows.Close()
		if want := [][4]string{{"Note", rowrefs.UUIDToDB(holder), "Ticket", "blocks"}}; !reflect.DeepEqual(cleared, want) {
			t.Fatalf("the cleared messages: %v", cleared)
		}
	})

	t.Run("an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it", func(t *testing.T) {
		row := ticket(t, "Bounds")
		ctx := as(alice)
		// Bytes that are not the ones declared are refused, and the file
		// goes.
		wrong := upload(t, "Ticket", row, "a.txt", "text/plain", []byte("other bytes"), hexOf([]byte("declared bytes")))
		_, err := db.Attachments().Complete(ctx, rowrefs.FileRef{Table: "Ticket", ID: wrong.ID}, files)
		if rowrefs.CodeOf(err) != rowrefs.CodeMismatch {
			t.Fatalf("a mismatched upload: %v", err)
		}
		if _, err := db.Attachments().Get(ctx, rowrefs.FileRef{Table: "Ticket", ID: wrong.ID}); !isNotFound(err) {
			t.Fatalf("the mismatched file: %v", err)
		}
		if deleted, err := db.Attachments().Release(context.Background(), wrong.ObjectKey, files); err != nil || !deleted || files.has(wrong.ObjectKey) {
			t.Fatalf("the mismatched object's release: %t, %v", deleted, err)
		}
		// A file past a ticket's 1 MiB, and a note's media type, are
		// refused.
		begin := func(table, key, name, mediaType string, size int64, seed string) error {
			_, err := db.Attachments().Begin(ctx, rowrefs.BeginInput{To: rowrefs.RowRef{Table: table, Key: key}, Name: name, MediaType: mediaType, Size: size, SHA256: hexOf([]byte(seed))}, files, 0)
			return err
		}
		if err := begin("Ticket", row, "big.bin", "application/octet-stream", 2*1024*1024, ""); rowrefs.CodeOf(err) != rowrefs.CodeTooLarge {
			t.Fatalf("a big file: %v", err)
		}
		owned := note(t, "Typed files", alice, "everyone")
		if err := begin("Note", owned, "a.zip", "application/zip", 10, ""); rowrefs.CodeOf(err) != rowrefs.CodeMediaType {
			t.Fatalf("a zip on a note: %v", err)
		}
		// Four files of 1 MiB fill a ticket's 4 MiB: a fifth is refused.
		for i := range 4 {
			if err := begin("Ticket", row, fmt.Sprintf("p%d.bin", i), "application/octet-stream", 1024*1024, strconv.Itoa(i)); err != nil {
				t.Fatal(err)
			}
		}
		if err := begin("Ticket", row, "p5.bin", "application/octet-stream", 1, "5"); rowrefs.CodeOf(err) != rowrefs.CodeRowFull {
			t.Fatalf("a full row: %v", err)
		}
		// A file filed onto a note keeps its object until the last file
		// goes.
		owner := ticket(t, "Owner")
		body := []byte("shared bytes")
		file, err := db.Attachments().Complete(ctx, rowrefs.FileRef{Table: "Ticket", ID: upload(t, "Ticket", owner, "shared.txt", "text/plain", body, hexOf(body)).ID}, files)
		if err != nil {
			t.Fatal(err)
		}
		moved, err := db.Attachments().File(ctx, rowrefs.FileInput{FileRef: rowrefs.FileRef{Table: "Ticket", ID: file.ID}, To: rowrefs.RowRef{Table: "Note", Key: owned}, Move: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Attachments().Get(ctx, rowrefs.FileRef{Table: "Ticket", ID: file.ID}); !isNotFound(err) {
			t.Fatalf("the moved file on its first row: %v", err)
		}
		if deleted, err := db.Attachments().Release(context.Background(), file.ObjectKey, files); err != nil || deleted {
			t.Fatalf("a release of an object a file names: %t, %v", deleted, err)
		}
		if err := db.Attachments().Detach(ctx, rowrefs.FileRef{Table: "Note", ID: moved.ID}); err != nil {
			t.Fatal(err)
		}
		rows, err := pool.Query(context.Background(), `SELECT reason FROM attachment_released_queue WHERE object_key = $1 ORDER BY created_at`, file.ObjectKey)
		if err != nil {
			t.Fatal(err)
		}
		var reasons []string
		for rows.Next() {
			var reason string
			if err := rows.Scan(&reason); err != nil {
				t.Fatal(err)
			}
			reasons = append(reasons, reason)
		}
		rows.Close()
		if !slices.Equal(reasons, []string{"expired", "detached", "detached"}) {
			t.Fatalf("the released messages: %v", reasons)
		}
		if deleted, err := db.Attachments().Release(context.Background(), file.ObjectKey, files); err != nil || !deleted || files.has(file.ObjectKey) {
			t.Fatalf("the last release: %t, %v", deleted, err)
		}
	})
}
