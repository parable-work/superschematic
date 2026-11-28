// fixture-providers-go-api's runtime providers end to end (D159, D184):
// a Go server whose config names a provider per slot, over the Go modules
// of examples/runtime-providers. TestGoProvidersEndToEnd writes this file
// into the generated API module and runs it, with the generated create.sql
// of fixture-references-db applied in a schema of its own on Postgres. Each
// provider talks to a stub service here.
package fixtureprovidersgoapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/parable-work/superschematic/runtime/http/go/bucket"
	"github.com/parable-work/superschematic/runtime/http/go/fileprocessing"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"

	api "example.com/schemas/api/fixture-providers-go-api"
	orm "example.com/schemas/orm/fixture-references-db"
	dbtypes "example.com/schemas/types/go/fixture-references-db"
)

const (
	relayToken  = "relay-token-value"
	accountAuth = "account-token-value"
	vaultToken  = "vault-token-value"
	password    = "correct-horse-battery-staple"
	bridgeKey   = "bridge-key-value"
)

// received is what a stub service was sent.
type received struct {
	mu    sync.Mutex
	calls []string
}

func (r *received) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *received) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// relayA is sms-relay-a's service: JSON under a Bearer token.
func relayA(t *testing.T, got *received) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+relayToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/messages":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			got.add("a:" + body["to"] + ":" + body["text"])
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "a-1", "status": "accepted"})
		case "/v1/health":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// relayB is sms-relay-b's service: form posts under an account, with basic
// authentication.
func relayB(t *testing.T, got *received) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account, token, ok := r.BasicAuth()
		if !ok || account != "ac-1" || token != accountAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/accounts/ac-1/send" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = r.ParseForm()
		got.add("b:" + r.PostForm.Get("To") + ":" + r.PostForm.Get("Body"))
		_ = json.NewEncoder(w).Encode(map[string]string{"sid": "b-1"})
	}))
	t.Cleanup(server.Close)
	return server
}

// vaultService is the password manager's service API vault-broker reads.
func vaultService(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+vaultToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		item := map[string]any{"id": "reports-api", "title": "Reports API", "urls": []string{"https://reports.test"}}
		switch r.URL.Path {
		case "/v1/vaults/ops/items":
			_ = json.NewEncoder(w).Encode([]any{item})
		case "/v1/vaults/ops/items/reports-api":
			item["fields"] = []map[string]string{{"label": "password", "value": password}}
			_ = json.NewEncoder(w).Encode(item)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func openDatabase(t *testing.T) *orm.Database {
	t.Helper()
	target := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("providers_go_%d", time.Now().UnixNano())
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

func jsonLogger() (*zap.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&buf), zap.DebugLevel)
	return zap.New(core), &buf
}

// sendNotice is the app's one way to send a notice: through whichever
// adapter slot SMS names, so swapping it is the config's alone.
func sendNotice(ctx context.Context, deps api.Deps, to, body string) (providers.SendResult, error) {
	sms, err := deps.Providers.Channel(ctx, "SMS")
	if err != nil {
		return providers.SendResult{}, err
	}
	return sms.Send(ctx, providers.OutboundMessage{To: to, Body: body})
}

// depsOf is the Deps the entrypoint builds of cfg: the providers
// ProvidersOf reads, and the database.
func depsOf(t *testing.T, cfg api.EnvConfig, db orm.DatabaseInterface, logger *zap.Logger) api.Deps {
	t.Helper()
	held, err := api.ProvidersOf(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	return api.Deps{Config: cfg, DB: db, Providers: held, Logger: logger}
}

func TestProviders(t *testing.T) {
	db := openDatabase(t)
	ctx := context.Background()
	got := &received{}
	a, b, vault := relayA(t, got), relayB(t, got), vaultService(t)
	base := api.EnvConfig{}
	base.Vault, base.VaultUrl, base.VaultVaults, base.VaultToken = "vault-broker", vault.URL, "ops", vaultToken
	base.Runner = "local-sandbox"
	base.Model = "stub-model"
	base.Previews = "text-preview"
	devCfg := base
	devCfg.Sms, devCfg.SmsUrl, devCfg.SmsToken = "sms-relay-a", a.URL, relayToken
	cloudCfg := base
	cloudCfg.Sms, cloudCfg.SmsUrl, cloudCfg.SmsAccount, cloudCfg.SmsToken = "sms-relay-b", b.URL, "ac-1", accountAuth

	t.Run("the credential provider and a channel adapter run with no change to core", func(t *testing.T) {
		logger, lines := jsonLogger()
		deps := depsOf(t, devCfg, db, logger)
		sent, err := sendNotice(ctx, deps, "+15550100", "Your note changed.")
		if err != nil || sent.ProviderMessageID != "a-1" || sent.State != providers.DeliveryQueued {
			t.Fatalf("the notice through sms-relay-a: %+v, %v", sent, err)
		}
		sms, _ := deps.Providers.Channel(ctx, "SMS")
		if health, err := sms.Health(ctx); err != nil || !health.OK {
			t.Fatalf("health: %+v, %v", health, err)
		}
		if _, ok := sms.(providers.ChannelInboundReader); !ok {
			t.Fatal("sms-relay-a reads no inbound messages")
		}
		broker, err := deps.Providers.Credentials(ctx, "VAULT")
		if err != nil {
			t.Fatal(err)
		}
		listed, err := broker.List(ctx, providers.CredentialQuery{Site: "https://reports.test"})
		if err != nil || len(listed) != 1 || listed[0].Ref != "vault://ops/reports-api/password" {
			t.Fatalf("the broker's list: %+v, %v", listed, err)
		}
		headers := http.Header{}
		var records []providers.CredentialUse
		use, err := providers.UseCredential(ctx, providers.UseCredentialInput{
			Provider:     broker,
			ProviderName: "vault-broker",
			Ref:          listed[0].Ref,
			Target:       providers.HeaderTarget("https://reports.test", "Authorization"),
			Runner:       headerRunner{headers},
			Record:       func(_ context.Context, use providers.CredentialUse) error { records = append(records, use); return nil },
		})
		if err != nil || headers.Get("Authorization") != password {
			t.Fatalf("the runner's header: %q, %v", headers.Get("Authorization"), err)
		}
		record, _ := json.Marshal(append(records, use))
		if printed := lines.String() + string(record) + fmt.Sprintf("%v %+v", use, records); strings.Contains(printed, password) || strings.Contains(printed, relayToken) || strings.Contains(printed, vaultToken) {
			t.Fatalf("a credential's value or a secret setting reached a log or a record:\n%s", printed)
		}
		if !strings.Contains(lines.String(), `"provider":"vault-broker"`) {
			t.Fatalf("the broker's log line names no provider:\n%s", lines)
		}
	})

	t.Run("swapping one SMS provider for another is only a config change", func(t *testing.T) {
		before := len(got.all())
		for _, cfg := range []api.EnvConfig{devCfg, cloudCfg} {
			if _, err := sendNotice(ctx, depsOf(t, cfg, db, zap.NewNop()), "+15550101", "Swapped."); err != nil {
				t.Fatalf("%s: %v", cfg.Sms, err)
			}
		}
		if calls := got.all()[before:]; !slices.Equal(calls, []string{"a:+15550101:Swapped.", "b:+15550101:Swapped."}) {
			t.Fatalf("the relays got %v", calls)
		}
	})

	t.Run("a config that names an unknown provider, one of another interface, or leaves a setting unset is refused at start", func(t *testing.T) {
		for _, c := range []struct {
			edit func(*api.EnvConfig)
			want string
		}{
			{func(c *api.EnvConfig) { c.Sms = "sms-relay-x" }, "slot SMS names provider sms-relay-x, which the server does not link"},
			{func(c *api.EnvConfig) { c.Model = "sms-relay-a" }, "slot MODEL takes a model provider, and provider sms-relay-a implements channel"},
			{func(c *api.EnvConfig) { c.SmsUrl = "" }, "provider sms-relay-a in slot SMS needs SMS_URL, which is not set"},
		} {
			cfg := devCfg
			c.edit(&cfg)
			_, err := api.ProvidersOf(cfg, zap.NewNop())
			var refused *providers.ConfigError
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), relayToken) {
				t.Errorf("refused %v, want %q", err, c.want)
			}
		}
		// A slot the environment leaves unset is no provider.
		cfg := devCfg
		cfg.Runner = ""
		deps := depsOf(t, cfg, db, zap.NewNop())
		if _, err := deps.Providers.Sandbox(ctx, "RUNNER"); !providers.IsNotConfigured(err) {
			t.Fatalf("an unset slot: %v", err)
		}
	})

	t.Run("each call into a provider runs as its own principal, which the ORM and the routes hold to", func(t *testing.T) {
		deps := depsOf(t, devCfg, db, zap.NewNop())
		within, err := deps.Providers.Within(ctx, "SMS")
		if err != nil {
			t.Fatal(err)
		}
		want := access.Principal{Subject: "extension:sms-relay-a", Kind: access.KindExtension}
		if caller, held := orm.PrincipalOf(within); !held || caller.Subject != want.Subject || caller.Kind != want.Kind {
			t.Fatalf("the ORM's principal within the provider: %+v, %t", caller, held)
		}
		if roles := session.GetRoles(within); len(roles) != 1 || !slices.Equal(roles[0].Permissions, []string{"messages.relay"}) {
			t.Fatalf("the provider's role: %+v", roles)
		}
		// The ORM holds the provider to the rows everyone may read.
		owner, _ := dbtypes.ParseIdentityUUID("00000000-0000-4000-8000-0000000000a1")
		note := func(audience string) string {
			row := dbtypes.NewNote()
			row.Title, row.Owner, row.Audience, row.CreatedAt = audience+" note", owner, audience, dbtypes.TemporalDateTime(time.Now())
			created, err := db.GetNoteRepository().CreateOne(orm.AsSystem(ctx), row)
			if err != nil {
				t.Fatal(err)
			}
			return created.Id.String()
		}
		open, private := note("everyone"), note("private")
		openID, _ := dbtypes.ParseIdentityUUID(open)
		privateID, _ := dbtypes.ParseIdentityUUID(private)
		seen, err := db.GetNoteRepository().GetManyByIDs(within, []dbtypes.IdentityUUID{openID, privateID})
		if err != nil || len(seen) != 1 || seen[openID] == nil {
			t.Fatalf("the provider read %d notes: %v", len(seen), err)
		}
		// A provider that calls the app with its key is its principal at
		// a generated route too (D180's caller).
		router := chi.NewRouter()
		router.Use(providers.ExtensionAuthenticator([]providers.ExtensionKey{{Key: providers.NewSecret(bridgeKey), Provider: "sms-relay-a", Role: []string{"messages.relay"}}}, nil))
		if err := api.RegisterRoutes(router, api.Config{DB: db, Logger: zap.NewNop()}); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(router)
		defer server.Close()
		ticket, err := db.GetTicketRepository().CreateOne(orm.AsSystem(ctx), &orm.TicketCreate{Title: "Relayed"})
		if err != nil {
			t.Fatal(err)
		}
		for token, status := range map[string]int{bridgeKey: http.StatusOK, "other": http.StatusUnauthorized} {
			req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/tickets/"+ticket.Id.String(), nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode != status {
				t.Fatalf("GET tickets/{id} with %s: %d %s", token, res.StatusCode, body)
			}
		}
	})

	t.Run("a sandbox, a model and a file processor of Go run through their slots", func(t *testing.T) {
		deps := depsOf(t, devCfg, db, zap.NewNop())
		runner, err := deps.Providers.Sandbox(ctx, "RUNNER")
		if err != nil {
			t.Fatal(err)
		}
		started, err := runner.Start(ctx, providers.SandboxSpec{RunID: "run-1", Command: []string{"sleep", "30"}, Secrets: map[string]providers.SecretValue{"RUN_TOKEN": providers.NewSecret("run-token")}})
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Cancel(ctx, started.ID); err != nil {
			t.Fatal(err)
		}
		if status, err := runner.Status(ctx, started.ID); err != nil || status.State != providers.SandboxCancelled {
			t.Fatalf("a cancelled run: %+v, %v", status, err)
		}
		model, err := deps.Providers.Model(ctx, "MODEL")
		if err != nil {
			t.Fatal(err)
		}
		turn := providers.ModelTurn{Messages: []providers.ModelMessage{{Role: providers.RoleUser, Content: `call notes.read {"id":"1"}`}}, Tools: []providers.ModelTool{{Name: "notes.read"}}}
		done, err := model.Complete(ctx, turn)
		if err != nil || done.Stop != providers.StopToolUse || len(done.ToolCalls) != 1 || done.ToolCalls[0].Name != "notes.read" {
			t.Fatalf("a tool call: %+v, %v", done, err)
		}
		turn.Messages[0].Untrusted = true
		if done, err := model.Complete(ctx, turn); err != nil || done.Stop != providers.StopEnd {
			t.Fatalf("untrusted content was obeyed: %+v, %v", done, err)
		}
		embedder, ok := model.(providers.ModelEmbedder)
		if !ok {
			t.Fatal("stub-model embeds nothing")
		}
		if embedded, err := embedder.Embed(ctx, providers.ModelEmbedRequest{Texts: []string{"a b", "b a"}}); err != nil || len(embedded.Vectors) != 2 || !slices.Equal(embedded.Vectors[0], embedded.Vectors[1]) {
			t.Fatalf("embeddings: %+v, %v", embedded, err)
		}
		files, err := deps.Providers.Files(ctx, "PREVIEWS")
		if err != nil {
			t.Fatal(err)
		}
		text, err := files.ExtractText(ctx, providers.FileInput{MediaType: "text/plain", Bytes: []byte("Hello <notes>")})
		if err != nil || text == nil || text.Text != "Hello <notes>" {
			t.Fatalf("extracted %+v, %v", text, err)
		}
		preview, err := files.Preview(ctx, providers.FileInput{MediaType: "text/plain", Bytes: []byte("Hello <notes>")}, providers.PreviewOptions{})
		if err != nil || preview == nil || preview.MediaType != "image/svg+xml" || !strings.Contains(string(preview.Bytes), "&#60;notes&#62;") {
			t.Fatalf("previewed %+v, %v", preview, err)
		}
		pgm := []byte("P2\n4 2\n255\n0 255 0 255\n255 0 255 0\n")
		thumb, err := files.Preview(ctx, providers.FileInput{MediaType: "image/x-portable-graymap", Bytes: pgm}, providers.PreviewOptions{MaxWidth: 2, MaxHeight: 2})
		if err != nil || thumb == nil || thumb.Width != 2 || thumb.Height != 1 {
			t.Fatalf("a thumbnail: %+v, %v", thumb, err)
		}
		if none, err := files.ExtractText(ctx, providers.FileInput{MediaType: "application/zip"}); err != nil || none != nil {
			t.Fatalf("a file the processor does not read: %+v, %v", none, err)
		}
	})

	t.Run("the server's Deps hold the database's references and attachments", func(t *testing.T) {
		deps := depsOf(t, devCfg, db, zap.NewNop())
		ticket, err := db.GetTicketRepository().CreateOne(orm.AsSystem(ctx), &orm.TicketCreate{Title: "Held"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := db.GetTicketRepository().CreateOne(orm.AsSystem(ctx), &orm.TicketCreate{Title: "Pointed at"})
		if err != nil {
			t.Fatal(err)
		}
		from, to := rowrefs.RowRef{Table: "Ticket", Key: ticket.Id.String()}, rowrefs.RowRef{Table: "Ticket", Key: other.Id.String()}
		if _, err := deps.DB.References().Add(orm.AsSystem(ctx), rowrefs.AddInput{From: from, To: to}); err != nil {
			t.Fatal(err)
		}
		backlinks, err := deps.DB.References().ReferencedBy(orm.AsSystem(ctx), to, rowrefs.ReferencedByOptions{})
		if err != nil || len(backlinks.Items) != 1 || backlinks.Items[0].Key != from.Key {
			t.Fatalf("the backlinks through Deps: %+v, %v", backlinks, err)
		}
		files, err := deps.DB.Attachments().List(orm.AsSystem(ctx), from)
		if err != nil || len(files) != 0 {
			t.Fatalf("the files through Deps: %+v, %v", files, err)
		}
	})

	t.Run("the file processor's pass makes an uploaded file's text and preview through the slot PREVIEWS names, and ends a file it fails on failed", func(t *testing.T) {
		logger, logged := jsonLogger()
		deps := depsOf(t, devCfg, db, logger)
		system := orm.AsSystem(ctx)
		objects := &memBucket{objects: map[string]memObject{}}
		wiring := api.Wiring{Files: fileprocessing.Options{Bucket: objects}}
		ticket, err := db.GetTicketRepository().CreateOne(system, &orm.TicketCreate{Title: "Plans"})
		if err != nil {
			t.Fatal(err)
		}
		row := rowrefs.RowRef{Table: "Ticket", Key: ticket.Id.String()}
		files := deps.DB.Attachments()
		plan := upload(system, t, files, objects, row, "plan.txt", "text/plain", []byte("Hawthorn rows along the north wall."))
		broken := upload(system, t, files, objects, row, "scan.pgm", "image/x-portable-graymap", []byte("not an image"))
		// The upload asked each file's text: text-preview reads the plan's,
		// and reads no text of a PGM image, which ends that file failed.
		run, err := api.ProcessPendingFiles(ctx, deps, wiring)
		if err != nil || run != (fileprocessing.Run{Extracted: 1, Failed: 1}) {
			t.Fatalf("the first pass: %+v, %v", run, err)
		}
		got, err := files.Get(system, rowrefs.FileRef{Table: "Ticket", ID: plan.ID})
		if err != nil || got.Extraction != rowrefs.ProcessingDone || got.ExtractedText == nil || *got.ExtractedText != "Hawthorn rows along the north wall." {
			t.Fatalf("the plan's text: %+v, %v", got, err)
		}
		// A first view asks a preview: the plan's is an SVG beside its
		// object, and text-preview fails on the image that is no PGM.
		for _, file := range []rowrefs.Attachment{plan, broken} {
			if _, err := files.Download(system, rowrefs.FileRef{Table: "Ticket", ID: file.ID}, bucket.ForAttachments(objects), 0); err != nil {
				t.Fatal(err)
			}
		}
		run, err = api.ProcessPendingFiles(ctx, deps, wiring)
		if err != nil || run != (fileprocessing.Run{Previewed: 1, Failed: 1}) {
			t.Fatalf("the second pass: %+v, %v", run, err)
		}
		got, err = files.Get(system, rowrefs.FileRef{Table: "Ticket", ID: plan.ID})
		if err != nil || got.Preview != rowrefs.ProcessingDone || got.PreviewObject == nil || *got.PreviewObject != rowrefs.PreviewObjectOf(plan.ObjectKey) {
			t.Fatalf("the plan's preview: %+v, %v", got, err)
		}
		if preview := objects.objects[*got.PreviewObject]; preview.contentType != "image/svg+xml" || !strings.Contains(string(preview.data), "Hawthorn") {
			t.Fatalf("the preview object: %q %q", preview.contentType, preview.data)
		}
		got, err = files.Get(system, rowrefs.FileRef{Table: "Ticket", ID: broken.ID})
		if err != nil || got.Extraction != rowrefs.ProcessingFailed || got.Preview != rowrefs.ProcessingFailed {
			t.Fatalf("the image the processor failed on: %+v, %v", got, err)
		}
		// Nothing is pending: a third pass asks the processor nothing.
		if run, err := api.ProcessPendingFiles(ctx, deps, wiring); err != nil || run != (fileprocessing.Run{}) {
			t.Fatalf("the third pass: %+v, %v", run, err)
		}
		// Each failure is logged with its reason, and never the file's text.
		lines := logged.String()
		if strings.Count(lines, "a file was not processed") != 2 || !strings.Contains(lines, "no file processor extracts the text of image/x-portable-graymap") || !strings.Contains(lines, "no PGM image") {
			t.Errorf("the failures logged:\n%s", lines)
		}
		if strings.Contains(lines, "Hawthorn") || strings.Contains(lines, "not an image") {
			t.Errorf("a file's bytes reached the log:\n%s", lines)
		}
		// A pass with no bucket is refused, saying so.
		if _, err := api.ProcessPendingFiles(ctx, deps, api.Wiring{}); err == nil || !strings.Contains(err.Error(), "wiring.Files") {
			t.Errorf("a pass with no bucket: %v", err)
		}
	})
}

// memBucket is a bucket in memory, which a browser's PUT through a
// signed URL writes as put does.
type memBucket struct {
	mu      sync.Mutex
	objects map[string]memObject
}

type memObject struct {
	data        []byte
	contentType string
}

func (b *memBucket) Name() string { return "mem" }

func (b *memBucket) Put(_ context.Context, name string, body io.Reader, opts bucket.PutOptions) (bucket.PutResult, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return bucket.PutResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[name] = memObject{data: data, contentType: opts.ContentType}
	return bucket.PutResult{Object: bucket.Object{Name: name, Size: int64(len(data)), ContentType: opts.ContentType}, Created: true}, nil
}

func (b *memBucket) Head(_ context.Context, name string) (bucket.Object, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.objects[name]
	return bucket.Object{Name: name, Size: int64(len(o.data)), ContentType: o.contentType}, ok, nil
}

func (b *memBucket) Get(_ context.Context, name string) (*bucket.Reader, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.objects[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", bucket.ErrNotFound, name)
	}
	return &bucket.Reader{ReadCloser: io.NopCloser(bytes.NewReader(o.data)), Object: bucket.Object{Name: name, Size: int64(len(o.data)), ContentType: o.contentType}}, nil
}

func (b *memBucket) Delete(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.objects[name]; !ok {
		return fmt.Errorf("%w: %s", bucket.ErrNotFound, name)
	}
	delete(b.objects, name)
	return nil
}

func (b *memBucket) List(context.Context, bucket.ListOptions) (bucket.Page, error) {
	return bucket.Page{}, nil
}

func (b *memBucket) SignedURL(_ context.Context, name string, _ bucket.SignedURLOptions) (string, error) {
	return "https://mem.example/" + name, nil
}

// upload is a browser's upload of data as a file of row: Begin, the PUT
// its URL takes, and Complete, which asks the file's text.
func upload(ctx context.Context, t *testing.T, files *rowrefs.Attachments, objects *memBucket, row rowrefs.RowRef, name, mediaType string, data []byte) rowrefs.Attachment {
	t.Helper()
	sum := sha256.Sum256(data)
	begun, err := files.Begin(ctx, rowrefs.BeginInput{To: row, Name: name, MediaType: mediaType, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, bucket.ForAttachments(objects), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Put(ctx, begun.Attachment.ObjectKey, bytes.NewReader(data), bucket.PutOptions{ContentType: mediaType}); err != nil {
		t.Fatal(err)
	}
	done, err := files.Complete(ctx, rowrefs.FileRef{Table: row.Table, ID: begun.Attachment.ID}, bucket.ForAttachments(objects))
	if err != nil {
		t.Fatal(err)
	}
	if done.Extraction != rowrefs.ProcessingPending {
		t.Fatalf("a completed upload's extraction is %s, not pending", done.Extraction)
	}
	return done
}

// headerRunner is a runner that sets request headers.
type headerRunner struct{ h http.Header }

func (headerRunner) Fill(context.Context, string, string) error { return errors.New("no browser") }
func (r headerRunner) SetHeader(_ context.Context, name, value string) error {
	r.h.Set(name, value)
	return nil
}
