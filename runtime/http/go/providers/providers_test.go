package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// TestContractsAreTheOnesTheBuildChecks holds the contracts to
// runtime_contracts.json, which ir writes and the TypeScript and Rust
// runtimes read, and each Go interface's methods to its contract's
// operations: a method per operation, optional ones on interfaces of their
// own.
func TestContractsAreTheOnesTheBuildChecks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runtime_contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Contracts []Contract `json:"contracts"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(Contracts(), file.Contracts) {
		t.Fatalf("Contracts() = %+v, runtime_contracts.json %+v", Contracts(), file.Contracts)
	}
	interfaces := map[Interface][]reflect.Type{
		Credentials: {reflect.TypeFor[CredentialProvider]()},
		Channel:     {reflect.TypeFor[ChannelAdapter](), reflect.TypeFor[ChannelStatusReader](), reflect.TypeFor[ChannelInboundReader]()},
		Sandbox:     {reflect.TypeFor[SandboxProvider]()},
		Model:       {reflect.TypeFor[ModelProvider]()},
		Files:       {reflect.TypeFor[FileProcessor]()},
	}
	for _, c := range file.Contracts {
		methods := map[string]bool{}
		for _, typ := range interfaces[Interface(c.Interface)] {
			for i := range typ.NumMethod() {
				methods[typ.Method(i).Name] = true
			}
		}
		for _, op := range append(slices.Clone(c.Required), c.Optional...) {
			if name := strings.ToUpper(op[:1]) + op[1:]; !methods[name] {
				t.Errorf("the Go %s has no method %s for operation %s", c.Title, name, op)
			}
		}
	}
}

// recording is a channel adapter that records what it sends and the
// caller each send ran within.
type recording struct {
	name  string
	url   string
	token SecretValue
	sent  *[]sent
}

type sent struct {
	message OutboundMessage
	url     string
	caller  access.Principal
	roles   []session.Role
}

func (r *recording) Kind() string { return "sms" }

func (r *recording) Send(ctx context.Context, message OutboundMessage) (SendResult, error) {
	*r.sent = append(*r.sent, sent{message, r.url, session.GetCaller(ctx), session.GetRoles(ctx)})
	return SendResult{ProviderMessageID: fmt.Sprintf("%s-%d", r.name, len(*r.sent)), State: DeliveryQueued}, nil
}

func (r *recording) Health(context.Context) (ChannelHealth, error) {
	return ChannelHealth{OK: r.token.Reveal() != ""}, nil
}

func recordingChannel(name string) (Definition, *[]sent) {
	var log []sent
	return MustDefine(Definition{
		Name:       name,
		Implements: Channel,
		Config:     map[string]SettingSpec{"URL": {}, "TOKEN": {Secret: true}, "SENDER": {Optional: true}},
		Role:       []string{"messages.relay"},
		Create: func(_ context.Context, settings Settings, pc Context) (any, error) {
			url, err := settings.Text("URL")
			if err != nil {
				return nil, err
			}
			token, err := settings.Secret("TOKEN")
			if err != nil {
				return nil, err
			}
			pc.Logger.Info("channel ready", zap.String("url", url), zap.Stringer("token", token), zap.Any("settings", settings))
			return &recording{name: name, url: url, token: token, sent: &log}, nil
		},
	}), &log
}

func jsonLogger() (*zap.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&buf), zap.DebugLevel)
	return zap.New(core), &buf
}

func TestFromGivesTheProviderASlotNamesRunningAsItsOwnPrincipal(t *testing.T) {
	a, sentA := recordingChannel("relay-a")
	b, sentB := recordingChannel("relay-b")
	logger, lines := jsonLogger()
	providers, err := From(Options{
		Slots:       map[string]Interface{"SMS": Channel, "VAULT": Credentials},
		Config:      map[string]ConfigValue{"SMS": Text("relay-b"), "SMS_URL": Text("http://relay.test"), "SMS_TOKEN": Secret("token-value-1")},
		Definitions: []Definition{a, b},
		Logger:      logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := providers.Configured(""), []ConfiguredProvider{{Slot: "SMS", Interface: Channel, Provider: "relay-b"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Configured = %+v, want %+v", got, want)
	}
	if got := providers.Configured(Credentials); len(got) != 0 {
		t.Fatalf("Configured(credentials) = %+v", got)
	}
	ctx := context.Background()
	sms, err := providers.Channel(ctx, "SMS")
	if err != nil {
		t.Fatal(err)
	}
	result, err := sms.Send(ctx, OutboundMessage{To: "+15550100", Body: "hello"})
	if err != nil || result != (SendResult{ProviderMessageID: "relay-b-1", State: DeliveryQueued}) {
		t.Fatalf("Send = %+v, %v", result, err)
	}
	if health, _ := sms.Health(ctx); !health.OK {
		t.Fatal("health is not ok")
	}
	if len(*sentA) != 0 || (*sentB)[0].url != "http://relay.test" {
		t.Fatalf("sent through a %d, b %+v", len(*sentA), *sentB)
	}
	want := access.Principal{Subject: "extension:relay-b", Kind: access.KindExtension}
	if got := (*sentB)[0].caller; !reflect.DeepEqual(got, want) {
		t.Fatalf("the send ran as %+v, want %+v", got, want)
	}
	if got := (*sentB)[0].roles; len(got) != 1 || !slices.Equal(got[0].Permissions, []string{"messages.relay"}) {
		t.Fatalf("the send ran with roles %+v", got)
	}
	if got, _ := providers.PrincipalOf("SMS"); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrincipalOf = %+v", got)
	}
	if again, _ := providers.Channel(ctx, "SMS"); again != sms {
		t.Fatal("a second Channel made the adapter again")
	}
	if _, ok := sms.(ChannelStatusReader); ok {
		t.Fatal("an adapter with no status reads statuses")
	}
	if strings.Contains(lines.String(), "token-value-1") {
		t.Fatalf("the log holds the secret: %s", lines)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.SplitN(lines.Bytes(), []byte("\n"), 2)[0], &line); err != nil {
		t.Fatal(err)
	}
	if line["provider"] != "relay-b" || line["slot"] != "SMS" || line["url"] != "http://relay.test" || line["token"] != SecretMask {
		t.Fatalf("the provider's log line = %v", line)
	}
	if _, err := providers.Credentials(ctx, "VAULT"); !IsNotConfigured(err) {
		t.Fatalf("an unset slot: %v", err)
	}
	if _, err := providers.Get(ctx, "SMS", Model); !IsNotConfigured(err) || !strings.Contains(err.Error(), "takes a channel adapter, not a model provider") {
		t.Fatalf("a slot of another interface: %v", err)
	}
	if _, err := providers.Get(ctx, "OTHER", Channel); !IsNotConfigured(err) || !strings.Contains(err.Error(), "no provider slot") {
		t.Fatalf("no slot: %v", err)
	}
}

func TestFromRefusesABadConfigNamingNoValue(t *testing.T) {
	a, _ := recordingChannel("relay-a")
	refused := func(config map[string]ConfigValue, slots map[string]Interface) []string {
		t.Helper()
		if slots == nil {
			slots = map[string]Interface{"SMS": Channel}
		}
		_, err := From(Options{Slots: slots, Config: config, Definitions: []Definition{a}})
		var problems *ConfigError
		if !errors.As(err, &problems) {
			t.Fatalf("not refused: %v", err)
		}
		return problems.Problems
	}
	for _, c := range []struct {
		config map[string]ConfigValue
		slots  map[string]Interface
		want   string
	}{
		{map[string]ConfigValue{"SMS": Text("relay-x")}, nil, "slot SMS names provider relay-x, which the server does not link (it links relay-a)"},
		{map[string]ConfigValue{"BOT": Text("relay-a")}, map[string]Interface{"BOT": Model}, "slot BOT takes a model provider, and provider relay-a implements channel"},
		{map[string]ConfigValue{"SMS": Text("relay-a"), "SMS_TOKEN": Secret("token-value-2")}, nil, "provider relay-a in slot SMS needs SMS_URL, which is not set"},
		{map[string]ConfigValue{"SMS": Text("relay-a"), "SMS_URL": Text("http://relay.test"), "SMS_TOKEN": Text("token-value-3")}, nil, "provider relay-a in slot SMS reads SMS_TOKEN as a secret, and the config holds it as no secret"},
		{map[string]ConfigValue{"SMS": Secret("relay-a")}, nil, "slot SMS holds no provider's name"},
	} {
		if got := refused(c.config, c.slots); !slices.Equal(got, []string{c.want}) {
			t.Errorf("problems = %q, want %q", got, c.want)
		}
		if got := strings.Join(refused(c.config, c.slots), ";"); strings.Contains(got, "token-value") {
			t.Errorf("a problem names a value: %s", got)
		}
	}
	// A provider the build read but the server does not link, and one
	// whose manifest is not its definition.
	_, err := From(Options{
		Slots:       map[string]Interface{"SMS": Channel},
		Definitions: []Definition{a},
		Manifests: map[string]Manifest{
			"relay-a": {Name: "relay-a", Implements: "channel", Config: map[string]ManifestItem{"URL": {}}, Role: []string{"messages.relay"}},
			"relay-z": {Name: "relay-z", Implements: "channel"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "its manifest's config") || !strings.Contains(err.Error(), "lists provider relay-z, which the server does not link") {
		t.Fatalf("manifests: %v", err)
	}
}

// half is a channel adapter that leaves out health.
type half struct{}

func (half) Kind() string { return "sms" }
func (half) Send(context.Context, OutboundMessage) (SendResult, error) {
	return SendResult{ProviderMessageID: "1", State: DeliverySent}, nil
}

func TestFromRefusesAProviderThatLeavesOutAnOperation(t *testing.T) {
	broken := MustDefine(Definition{Name: "half", Implements: Channel, Create: func(context.Context, Settings, Context) (any, error) { return half{}, nil }})
	providers, err := From(Options{Slots: map[string]Interface{"SMS": Channel}, Config: map[string]ConfigValue{"SMS": Text("half")}, Definitions: []Definition{broken}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providers.Channel(context.Background(), "SMS"); err == nil || !strings.Contains(err.Error(), "implements no send, health, which every channel adapter implements") {
		t.Fatalf("a half adapter: %v", err)
	}
}

func TestDefineHoldsADefinitionToTheBuildsRules(t *testing.T) {
	create := func(context.Context, Settings, Context) (any, error) { return nil, nil }
	for _, c := range []struct {
		d    Definition
		want string
	}{
		{Definition{Name: "Relay", Implements: Channel, Create: create}, "lower-case"},
		{Definition{Name: "relay", Implements: "payments", Create: create}, "which is no runtime interface"},
		{Definition{Name: "relay", Implements: Channel, Role: []string{"messages"}, Create: create}, "names an area"},
		{Definition{Name: "relay", Implements: Channel, Config: map[string]SettingSpec{"url": {}}, Create: create}, "upper-case"},
		{Definition{Name: "relay", Implements: Channel, Config: map[string]SettingSpec{CallerKey: {}}, Create: create}, "the key the provider calls the app with"},
		{Definition{Name: "relay", Implements: Channel}, "create is not a function"},
	} {
		var contract *ContractError
		if _, err := Define(c.d); !errors.As(err, &contract) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Define(%s) = %v, want %q", c.d.Name, err, c.want)
		}
	}
}

// statusRecording is a recording adapter that reads statuses too.
type statusRecording struct{ *recording }

func (statusRecording) Status(context.Context, WebhookRequest) ([]DeliveryStatus, error) {
	return nil, nil
}

func TestCheckManifestHoldsAManifestToTheDefinitionAndItsProvider(t *testing.T) {
	definition, _ := recordingChannel("relay-a")
	manifest := Manifest{Name: "relay-a", Implements: "channel", Config: map[string]ManifestItem{"URL": {}, "TOKEN": {Secret: true}, "SENDER": {Optional: true}}, Role: []string{"messages.relay"}}
	providers, err := From(Options{Slots: map[string]Interface{"SMS": Channel}, Config: map[string]ConfigValue{"SMS": Text("relay-a"), "SMS_URL": Text("u"), "SMS_TOKEN": Secret("t")}, Definitions: []Definition{definition}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := providers.Channel(context.Background(), "SMS")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckManifest(manifest, definition, instance); err != nil {
		t.Fatal(err)
	}
	listed := manifest
	listed.Operations = []string{"send", "health", "inbound"}
	if err := CheckManifest(listed, definition, instance); err == nil || !strings.Contains(err.Error(), "lists operations") {
		t.Fatalf("listed operations: %v", err)
	}
	listed.Operations = []string{"send", "health", "status"}
	if err := CheckManifest(listed, definition, statusRecording{&recording{}}); err != nil {
		t.Fatalf("an optional operation the provider has: %v", err)
	}
	role := manifest
	role.Role = []string{"messages.send"}
	if err := CheckManifest(role, definition, nil); err == nil || !strings.Contains(err.Error(), "manifest's role") {
		t.Fatalf("role: %v", err)
	}

	dir := t.TempDir()
	if _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), "declares no provider") {
		t.Fatalf("no manifest: %v", err)
	}
	doc, _ := json.Marshal(map[string]any{"provider": manifest})
	if err := os.WriteFile(filepath.Join(dir, "superschematic.json"), doc, 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := ReadManifest(dir)
	if err != nil || CheckManifest(read, definition, instance) != nil {
		t.Fatalf("superschematic.json: %+v, %v", read, err)
	}
	pkg, _ := json.Marshal(map[string]any{"name": "@example/relay-a", "superschematic": map[string]any{"provider": role}})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), pkg, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), "declare different providers") {
		t.Fatalf("two declarations that differ: %v", err)
	}
}

const credentialValue = "correct-horse-battery-staple"

type vault struct{ ref, site string }

func (vault) List(context.Context, CredentialQuery) ([]CredentialListing, error) {
	return []CredentialListing{{Ref: "ops/reports-api/token", Title: "Reports API", Sites: []string{"https://reports.test"}}}, nil
}

func (v vault) Resolve(_ context.Context, ref string, into InjectionTarget) (*Injection, error) {
	if into.Kind != TargetHeader {
		return nil, errors.New("header only")
	}
	if v.ref != "" {
		ref = v.ref
	}
	site := into.Site
	if v.site != "" {
		site = v.site
	}
	return HeaderInjection(ref, site, into.Name, NewSecret("Bearer "+credentialValue)), nil
}

type forged struct{}

func (forged) List(context.Context, CredentialQuery) ([]CredentialListing, error) { return nil, nil }
func (forged) Resolve(context.Context, string, InjectionTarget) (*Injection, error) {
	return &Injection{}, nil
}

type headers map[string]string

func (headers) Fill(context.Context, string, string) error { return nil }
func (h headers) SetHeader(_ context.Context, name, value string) error {
	if h == nil {
		return errors.New("the runner must not be reached")
	}
	h[name] = value
	return nil
}

func TestACredentialReachesTheRunnerAlone(t *testing.T) {
	target := HeaderTarget("https://reports.test", "Authorization")
	runner := headers{}
	var records []CredentialUse
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	use, err := UseCredential(context.Background(), UseCredentialInput{
		Provider:     vault{},
		ProviderName: "vault-broker",
		Ref:          "ops/reports-api/token",
		Target:       target,
		Runner:       runner,
		Record:       func(_ context.Context, use CredentialUse) error { records = append(records, use); return nil },
		Now:          func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner["Authorization"] != "Bearer "+credentialValue {
		t.Fatalf("the runner got %q", runner["Authorization"])
	}
	if want := (CredentialUse{Ref: "ops/reports-api/token", Provider: "vault-broker", Target: target, UsedAt: at}); use != want || len(records) != 1 || records[0] != want {
		t.Fatalf("use = %+v, records %+v", use, records)
	}
	injection, _ := vault{}.Resolve(context.Background(), "ops/reports-api/token", target)
	logger, lines := jsonLogger()
	logger.Info("credential used", zap.Any("use", use), zap.Any("injection", injection), zap.Stringer("stringer", injection))
	logger.Error("step failed", zap.Error(fmt.Errorf("%v", injection)))
	asJSON, _ := json.Marshal(injection)
	recorded, _ := json.Marshal(records)
	printed := strings.Join([]string{lines.String(), string(asJSON), fmt.Sprint(injection), fmt.Sprintf("%+v %#v %s %q %x", injection, injection, injection, injection, injection), fmt.Sprintf("%+v %#v", *injection, *injection), string(recorded)}, "\n")
	if strings.Contains(printed, credentialValue) {
		t.Fatalf("a credential's value was printed:\n%s", printed)
	}
	var doc map[string]any
	_ = json.Unmarshal(asJSON, &doc)
	if doc["value"] != SecretMask || doc["ref"] != "ops/reports-api/token" {
		t.Fatalf("the injection's JSON = %s", asJSON)
	}
}

func TestUseCredentialRefusesAnInjectionItDidNotSealOrOfAnotherReferenceOrTarget(t *testing.T) {
	target := HeaderTarget("https://reports.test", "Authorization")
	ctx := context.Background()
	use := func(p CredentialProvider) error {
		_, err := UseCredential(ctx, UseCredentialInput{Provider: p, ProviderName: "p", Ref: "ops/reports-api/token", Target: target, Runner: headers(nil)})
		return err
	}
	var injectionErr *InjectionError
	if err := use(forged{}); !errors.As(err, &injectionErr) {
		t.Fatalf("forged: %v", err)
	}
	if err := use(vault{ref: "ops/other"}); err == nil || !strings.Contains(err.Error(), "an injection for ops/other") {
		t.Fatalf("swapped: %v", err)
	}
	if err := use(vault{site: "https://elsewhere.test"}); err == nil || !strings.Contains(err.Error(), "into another target") {
		t.Fatalf("redirected: %v", err)
	}
	if err := ApplyInjection(ctx, &Injection{}, headers(nil)); !errors.As(err, &injectionErr) {
		t.Fatalf("an Injection{} applied: %v", err)
	}
}

func TestAnExtensionAuthenticatesByItsKeyWithItsRoleAlone(t *testing.T) {
	keys := []ExtensionKey{{Key: NewSecret("bridge-key"), Provider: "chat-bridge", Role: []string{"messages.relay"}}}
	var seen access.Principal
	route := func(permission string) http.Handler {
		return ExtensionAuthenticator(keys, nil)(session.RequirePermissions(permission)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = session.GetCaller(r.Context())
			w.WriteHeader(http.StatusNoContent)
		})))
	}
	call := func(permission, token string) int {
		r := httptest.NewRequest(http.MethodPost, "/relay", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		route(permission).ServeHTTP(w, r)
		return w.Code
	}
	if code := call("messages.relay", "bridge-key"); code != http.StatusNoContent || !reflect.DeepEqual(seen, ExtensionPrincipal("chat-bridge")) {
		t.Fatalf("the bridge's key: %d as %+v", code, seen)
	}
	if code := call("notes.write", "bridge-key"); code != http.StatusForbidden {
		t.Fatalf("a permission past its role: %d", code)
	}
	if code := call("messages.relay", "other"); code != http.StatusUnauthorized {
		t.Fatalf("another key: %d", code)
	}
}

// TestExtensionKeysOfAreTheSlotsWhoseProviderCallsTheApp (#744): a slot
// whose provider lists CALLER_KEY and whose config sets it gives an entry
// of the provider's name and role; a slot whose provider lists none, or
// whose key is unset, gives none. A request bearing the key runs as the
// provider, and any other goes on to the auth step inside.
func TestExtensionKeysOfAreTheSlotsWhoseProviderCallsTheApp(t *testing.T) {
	key := fmt.Sprintf("key-%d", time.Now().UnixNano())
	create := func(context.Context, Settings, Context) (any, error) { return nil, nil }
	bridge := MustDefine(Definition{Name: "chat-bridge", Implements: Channel, Role: []string{"messages.relay"}, Create: create,
		Config: map[string]SettingSpec{CallerKey: {Secret: true}, "TOKEN": {Secret: true}}})
	relay := MustDefine(Definition{Name: "sms-relay", Implements: Channel, Role: []string{"messages.relay"}, Create: create,
		Config: map[string]SettingSpec{"TOKEN": {Secret: true}}})
	options := Options{
		Slots: map[string]Interface{"CHAT": Channel, "SMS": Channel, "OTHER": Channel},
		Config: map[string]ConfigValue{
			"CHAT": Text("chat-bridge"), "CHAT_CALLER_KEY": Secret(key), "CHAT_TOKEN": Secret("t"),
			"SMS": Text("sms-relay"), "SMS_TOKEN": Secret("t"), "SMS_CALLER_KEY": Secret("unread"),
		},
		Definitions: []Definition{bridge, relay},
	}
	keys := ExtensionKeysOf(options)
	if len(keys) != 1 || keys[0].Provider != "chat-bridge" || !reflect.DeepEqual(keys[0].Role, []string{"messages.relay"}) || keys[0].Key.Reveal() != key {
		t.Fatalf("ExtensionKeysOf = %+v, want chat-bridge's alone", keys)
	}
	if printed := fmt.Sprintf("%v %+v %#v", keys, keys, keys); strings.Contains(printed, key) {
		t.Fatal("a printed key shows its value")
	}
	unset := options
	unset.Config = maps.Clone(options.Config)
	delete(unset.Config, "CHAT_CALLER_KEY")
	if keys := ExtensionKeysOf(unset); len(keys) != 0 {
		t.Fatalf("a slot whose key is unset gives %+v", keys)
	}

	inner := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(session.ContextWithCaller(r.Context(), access.Principal{Subject: "user-1", Kind: access.KindUser})))
		})
	}
	var seen access.Principal
	handler := ExtensionAuthenticator(keys, inner)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = session.GetCaller(r.Context())
	}))
	for token, want := range map[string]string{key: "extension:chat-bridge", "another token": "user-1"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		handler.ServeHTTP(httptest.NewRecorder(), r)
		if seen.Subject != want {
			t.Errorf("a request bearing %s runs as %q, want %q", map[bool]string{true: "the key", false: "another token"}[token == key], seen.Subject, want)
		}
	}
}

func TestReadsMediaType(t *testing.T) {
	for _, c := range []struct {
		types []string
		kind  string
		want  bool
	}{
		{[]string{"image/*"}, "image/png", true},
		{[]string{"text/plain"}, "text/plain; charset=utf-8", true},
		{[]string{"text/plain"}, "text/html", false},
		{[]string{"IMAGE/*"}, "Image/JPEG", true},
	} {
		if got := ReadsMediaType(c.types, c.kind); got != c.want {
			t.Errorf("ReadsMediaType(%v, %s) = %t", c.types, c.kind, got)
		}
	}
}
