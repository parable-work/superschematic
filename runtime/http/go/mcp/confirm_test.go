package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/http/go/routing"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// The confirmation of a call of a tool whose invocation policy is ask
// (D93), as the TypeScript runtime's mcpconfirm.test.ts and the Rust
// runtime's tests/mcp_confirm.rs hold it: tokens the TypeScript runtime
// minted (runtime/http/rust/tests/testdata/mcp/confirm_vectors.json)
// redeem here, once, and the canonical JSON they bind is written alike;
// and the endpoint of a router asks before it runs, and asks no one about
// a call its route would refuse (D98).

const vectorsPath = "../../rust/tests/testdata/mcp/confirm_vectors.json"

type vectorFile struct {
	Key        string `json:"key"`
	Now        int64  `json:"now"`
	TTLSeconds int    `json:"ttlSeconds"`
	Canonical  []struct {
		JSON      string `json:"json"`
		Canonical string `json:"canonical"`
	} `json:"canonical"`
	Tokens []struct {
		Tool, Caller, Scope, Mode, Args, Token, ExpiresAt string
	} `json:"tokens"`
}

func loadVectors(t *testing.T) vectorFile {
	data, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var vectors vectorFile
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func argsOf(t *testing.T, text string) map[string]any {
	value, err := decodeJSON([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return value.(map[string]any)
}

func TestCanonicalJSONIsTheTypeScriptRuntimes(t *testing.T) {
	for _, c := range loadVectors(t).Canonical {
		value, err := decodeJSON([]byte(c.JSON))
		if err != nil {
			t.Fatal(err)
		}
		if got := CanonicalJSON(value); got != c.Canonical {
			t.Errorf("CanonicalJSON(%s) = %s, want %s", c.JSON, got, c.Canonical)
		}
	}
}

func TestATokenTheTypeScriptRuntimeMintedRedeemsHereOnce(t *testing.T) {
	vectors := loadVectors(t)
	now := time.UnixMilli(vectors.Now)
	for _, token := range vectors.Tokens {
		confirmations, err := NewConfirmations(ConfirmationOptions{Key: []byte(vectors.Key), Scope: token.Scope, TTL: time.Duration(vectors.TTLSeconds) * time.Second, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		binding := ConfirmationBinding{Tool: token.Tool, Caller: token.Caller, Args: argsOf(t, token.Args)}
		other := ModeElicit
		if token.Mode == string(ModeElicit) {
			other = ModeToken
		}
		ctx := context.Background()
		if refused, _ := confirmations.Redeem(ctx, token.Token, other, binding); refused != RefusedMismatch {
			t.Errorf("%s in the other mode = %q, want mismatch", token.Tool, refused)
		}
		if refused, _ := confirmations.Redeem(ctx, token.Token, ConfirmationMode(token.Mode), binding); refused != "" {
			t.Errorf("%s = %q, want it redeemed", token.Tool, refused)
		}
		if refused, _ := confirmations.Redeem(ctx, token.Token, ConfirmationMode(token.Mode), binding); refused != RefusedUsed {
			t.Errorf("%s again = %q, want used", token.Tool, refused)
		}
		minted, err := confirmations.Mint(ModeToken, binding)
		if err != nil {
			t.Fatal(err)
		}
		if minted.ExpiresAt != token.ExpiresAt {
			t.Errorf("a token minted at the vectors' clock expires at %s, want %s", minted.ExpiresAt, token.ExpiresAt)
		}
		if parts := strings.Split(minted.Token, "."); parts[4] != strings.Split(token.Token, ".")[4] {
			t.Errorf("%s binds its call as %s, the TypeScript runtime as %s", token.Tool, parts[4], strings.Split(token.Token, ".")[4])
		}
	}
}

func TestATokenRefusesAnotherCallAKeyItWasNotMintedWithAndItsEnd(t *testing.T) {
	key := []byte("kkkkkkkkkkkkkkkkeeeeeeeeeeeeeeeey")
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	confirmations, _ := NewConfirmations(ConfirmationOptions{Key: key, Scope: "items-api", TTL: time.Minute, Now: clock})
	binding := ConfirmationBinding{Tool: "rename_item", Caller: `["ada",null]`, Args: map[string]any{"id": "i1"}}
	minted, _ := confirmations.Mint(ModeToken, binding)
	ctx := context.Background()
	other := binding
	other.Args = map[string]any{"id": "i2"}
	if refused, _ := confirmations.Redeem(ctx, minted.Token, ModeToken, other); refused != RefusedMismatch {
		t.Errorf("another call's = %q, want mismatch", refused)
	}
	someone := binding
	someone.Caller = `["bo",null]`
	if refused, _ := confirmations.Redeem(ctx, minted.Token, ModeToken, someone); refused != RefusedMismatch {
		t.Errorf("another caller's = %q, want mismatch", refused)
	}
	foreign, _ := NewConfirmations(ConfirmationOptions{Key: []byte("kkkkkkkkkkkkkkkkeeeeeeeeeeeeeeeez"), Scope: "items-api", Now: clock})
	if refused, _ := foreign.Redeem(ctx, minted.Token, ModeToken, binding); refused != RefusedInvalid {
		t.Errorf("under another key = %q, want invalid", refused)
	}
	for _, tampered := range []any{strings.Replace(minted.Token, "v1.token.", "v1.elicit.", 1), minted.Token + "x", "v1.token", 7} {
		if refused, _ := confirmations.Redeem(ctx, tampered, ModeToken, binding); refused != RefusedInvalid {
			t.Errorf("%v = %q, want invalid", tampered, refused)
		}
	}
	now = now.Add(time.Minute)
	if refused, _ := confirmations.Redeem(ctx, minted.Token, ModeToken, binding); refused != RefusedExpired {
		t.Errorf("at its end = %q, want expired", refused)
	}
	if _, err := NewConfirmations(ConfirmationOptions{Key: []byte("short")}); err == nil {
		t.Error("a short key was taken")
	}
	if _, err := NewConfirmations(ConfirmationOptions{Key: key, TTL: 1500 * time.Millisecond}); err == nil {
		t.Error("a TTL of no whole seconds was taken")
	}
	store := NewMemoryConfirmationStore(1)
	if ok, _ := store.Claim(ctx, "a", time.Second, now); !ok {
		t.Error("a first claim was refused")
	}
	if ok, _ := store.Claim(ctx, "b", time.Second, now); ok {
		t.Error("a full store forgot a live claim")
	}
	if ok, _ := store.Claim(ctx, "b", time.Second, now.Add(2*time.Second)); !ok {
		t.Error("a full store of expired claims refused a new one")
	}
}

// items is a router as a generated one makes it: a route per operation,
// whose handler decodes its request and stops at CheckContext for a
// route's check, and the endpoint over them.
type items struct {
	mux     *chi.Mux
	renames atomic.Int64
	calls   []*http.Request
}

const itemTools = `[
  {"name": "rename_item", "title": "Rename an item", "description": "Renames an item.", "inputSchema": {"type": "object", "properties": {"id": {"type": "string"}, "newName": {"type": "string"}}, "required": ["id", "newName"]}, "annotations": {"readOnlyHint": false}, "_meta": {"invocationPolicy": "ask"}, "operation": "items.rename", "method": "POST", "path": "/api/items/{id}/rename", "arguments": [{"key": "id", "in": "path", "name": "id"}, {"key": "newName", "in": "body", "name": "newName"}], "confirm": true, "autonomy": {"class": "moneyMovement"}},
  {"name": "get_item", "title": "Get an item", "description": "Reads an item.", "inputSchema": {"type": "object", "properties": {"id": {"type": "string"}, "where": {"type": "object"}}}, "annotations": {"readOnlyHint": true}, "_meta": {"invocationPolicy": "auto"}, "operation": "items.get", "method": "GET", "path": "/api/items/{id}", "arguments": [{"key": "id", "in": "path", "name": "id"}, {"key": "where", "in": "filter", "name": "filter"}], "idempotent": {}},
  {"name": "purge", "title": "Purge", "description": "Purges.", "inputSchema": {"type": "object"}, "_meta": {"invocationPolicy": "auto"}, "operation": "items.purge", "method": "DELETE", "path": "/api/items", "arguments": []}
]`

func newItems(t *testing.T, opts EndpointOptions) *items {
	it := &items{mux: chi.NewRouter()}
	it.mux.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token, ok := session.BearerToken(r.Header); ok {
				ctx := session.ContextWithPrincipalID(r.Context(), token)
				if token == "admin" {
					ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "admin", Permissions: []string{"items.purge"}}})
				}
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	})
	envelope := func(w http.ResponseWriter, data any) {
		response.JSON(w, http.StatusOK, map[string]any{"data": data, "meta": map[string]any{"requestId": "r1"}})
	}
	it.mux.Post("/api/items/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NewName *string `json:"newName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NewName == nil || *body.NewName == "" {
			response.Error(w, http.StatusBadRequest, "newName is required", "BAD_REQUEST")
			return
		}
		if err := requestctx.CheckContext(r.Context()); err != nil {
			apperror.Respond(w, nil, err)
			return
		}
		it.renames.Add(1)
		envelope(w, map[string]any{"id": chi.URLParam(r, "id"), "name": *body.NewName})
	})
	it.mux.Get("/api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		it.calls = append(it.calls, r)
		id, _ := routing.PathParam(r, "id")
		envelope(w, map[string]any{"id": id, "query": r.URL.RawQuery, "key": r.Header.Get("Idempotency-Key")})
	})
	it.mux.With(session.RequirePermissions("items.purge")).Delete("/api/items", func(w http.ResponseWriter, r *http.Request) {
		envelope(w, map[string]any{"purged": true})
	})
	tools, err := ParseRouteTools(itemTools)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := Mount{
		API: "items-api", Version: "1.0.0", RequiresAuth: true, Tools: tools, Routes: it.mux,
		Permits: map[string]func(http.Handler) http.Handler{"purge": session.RequirePermissions("items.purge")},
		Checked: map[string]bool{"rename_item": true},
	}.Handler(opts)
	if err != nil {
		t.Fatal(err)
	}
	it.mux.Handle(Path, handler)
	return it
}

var testKey = []byte("kkkkkkkkkkkkkkkkeeeeeeeeeeeeeeeey")

// rpc posts one request as caller and reads its one answer.
func (it *items) rpc(t *testing.T, caller, body string, modern bool) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if modern {
		r.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	}
	if caller != "" {
		r.Header.Set("Authorization", "Bearer "+caller)
	}
	w := httptest.NewRecorder()
	it.mux.ServeHTTP(w, r)
	text := w.Body.String()
	if strings.HasPrefix(text, "event: message\ndata: ") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "event: message\ndata: "), "\n\n")
	}
	var answer map[string]any
	_ = json.Unmarshal([]byte(text), &answer)
	return w.Code, answer
}

func (it *items) call(t *testing.T, caller, tool, arguments string) map[string]any {
	t.Helper()
	_, answer := it.rpc(t, caller, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+arguments+`}}`, false)
	result, _ := answer["result"].(map[string]any)
	if result == nil {
		t.Fatalf("tools/call %s answered %v", tool, answer)
	}
	return result
}

func structured(result map[string]any) map[string]any {
	out, _ := result["structuredContent"].(map[string]any)
	return out
}

func TestARoutersEndpointAsksBeforeItRunsAndAsksNoOneAboutACallItsRouteRefuses(t *testing.T) {
	it := newItems(t, EndpointOptions{ConfirmationKey: testKey})
	if status, _ := it.rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false); status != http.StatusUnauthorized {
		t.Errorf("tools/list with no caller = %d, want 401", status)
	}
	_, listed := it.rpc(t, "ada", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false)
	tools := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("ada is offered %v, want rename_item and get_item, not purge", tools)
	}
	rename := tools[0].(map[string]any)
	properties := rename["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if properties[ConfirmationArgument] == nil || properties[IdempotencyArgument] != nil {
		t.Errorf("rename_item takes %v, want its confirmation argument and no key", properties)
	}
	if get := tools[1].(map[string]any)["inputSchema"].(map[string]any)["properties"].(map[string]any); get[IdempotencyArgument] == nil {
		t.Errorf("get_item, @idempotent, takes %v, want its key argument", get)
	}
	_, admin := it.rpc(t, "admin", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false)
	if got := len(admin["result"].(map[string]any)["tools"].([]any)); got != 3 {
		t.Errorf("admin is offered %d tools, want 3", got)
	}

	asked := it.call(t, "ada", "rename_item", `{"id":"i1","newName":"Desk"}`)
	problem := structured(asked)
	details, _ := problem["details"].(map[string]any)
	token, _ := details[ConfirmationArgument].(string)
	if asked["isError"] != true || problem["status"] != float64(428) || problem["code"] != ConfirmationRequired || token == "" || it.renames.Load() != 0 {
		t.Fatalf("the first call answered %v and renamed %d, want confirmation_required with a token and nothing run", asked, it.renames.Load())
	}
	if summary := details["summary"]; summary != `Rename an item (rename_item) with {"id":"i1","newName":"Desk"}` {
		t.Errorf("the summary is %v", summary)
	}
	// The route refuses an empty name: the call answers its 400 at once,
	// and the token sent with it is not spent (D98).
	refused := it.call(t, "ada", "rename_item", `{"id":"i1","newName":"","confirmationToken":"`+token+`"}`)
	if p := structured(refused); refused["isError"] != true || p["status"] != float64(400) || p["detail"] != "newName is required" {
		t.Errorf("a call the route refuses answered %v, want its 400", refused)
	}
	if extra := structured(it.call(t, "ada", "rename_item", `{"id":"i1","newName":"Desk","colour":"red"}`)); extra["detail"] != `rename_item takes id, newName, not "colour"` {
		t.Errorf("an argument the tool does not take answered %v", extra)
	}
	if other := structured(it.call(t, "bo", "rename_item", `{"id":"i1","newName":"Desk","confirmationToken":"`+token+`"}`)); other["code"] != ConfirmationRequired || other["details"].(map[string]any)["refused"] != "mismatch" {
		t.Errorf("another caller's token answered %v, want it asked again as a mismatch", other)
	}
	ran := it.call(t, "ada", "rename_item", `{"id":"i1","newName":"Desk","confirmationToken":"`+token+`"}`)
	if got := string(marshal(structured(ran))); got != `{"id":"i1","name":"Desk"}` || it.renames.Load() != 1 {
		t.Errorf("the call with its token answered %v and renamed %d", ran, it.renames.Load())
	}
	if text := ran["content"].([]any)[0].(map[string]any)["text"]; text != `{"id":"i1","name":"Desk"}` {
		t.Errorf("its text is %v", text)
	}
	again := structured(it.call(t, "ada", "rename_item", `{"id":"i1","newName":"Desk","confirmationToken":"`+token+`"}`))
	if again["code"] != ConfirmationRequired || again["details"].(map[string]any)["refused"] != "used" || it.renames.Load() != 1 {
		t.Errorf("a used token answered %v, want it asked again", again)
	}

	// A 2026-07-28 client that elicits is asked in band.
	envelope := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"form":{}}}}`
	_, input := it.rpc(t, "ada", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"rename_item","arguments":{"id":"i2","newName":"Lamp"},`+envelope+`}}`, true)
	result := input["result"].(map[string]any)
	state, _ := result["requestState"].(string)
	if result["resultType"] != "input_required" || state == "" {
		t.Fatalf("an eliciting client was answered %v", input)
	}
	_, declined := it.rpc(t, "ada", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rename_item","arguments":{"id":"i2","newName":"Lamp"},"requestState":"`+state+`","inputResponses":{"confirm":{"action":"decline"}},`+envelope+`}}`, true)
	if p := structured(declined["result"].(map[string]any)); p["code"] != ConfirmationDeclined || it.renames.Load() != 1 {
		t.Errorf("a decline answered %v", declined)
	}
	_, input = it.rpc(t, "ada", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"rename_item","arguments":{"id":"i2","newName":"Lamp"},`+envelope+`}}`, true)
	state = input["result"].(map[string]any)["requestState"].(string)
	_, accepted := it.rpc(t, "ada", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"rename_item","arguments":{"id":"i2","newName":"Lamp"},"requestState":"`+state+`","inputResponses":{"confirm":{"action":"accept"}},`+envelope+`}}`, true)
	if got := accepted["result"].(map[string]any); got["resultType"] != "complete" || it.renames.Load() != 2 {
		t.Errorf("an accept answered %v", accepted)
	}

	// A tool the caller has not got is invalid params.
	_, unknown := it.rpc(t, "ada", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"purge","arguments":{}}}`, false)
	if got := string(marshal(unknown["error"])); got != `{"code":-32602,"message":"items-api has no tool \"purge\""}` {
		t.Errorf("a tool ada has not got answered %s", got)
	}
}

func TestACallsRequestCarriesItsArgumentsWhereTheRouteReadsThem(t *testing.T) {
	it := newItems(t, EndpointOptions{ConfirmationKey: testKey})
	got := structured(it.call(t, "ada", "get_item", `{"id":"a b/c","where":{"name":"Hex bolt","quantity":{"in":[3,4],"gte":2.0}},"idempotencyKey":"k1"}`))
	if got["id"] != "a b/c" || got["key"] != "k1" {
		t.Errorf("get_item read %v", got)
	}
	if query := fmt.Sprint(got["query"]); query != "filter%5Bname%5D=Hex+bolt&filter%5Bquantity%5D%5Bgte%5D=2&filter%5Bquantity%5D%5Bin%5D=3%2C4" {
		t.Errorf("the filter went as %s", query)
	}
	if call := it.calls[0]; call.Header.Get("Authorization") != "Bearer ada" || call.Header.Get("Mcp-Protocol-Version") != "" {
		t.Errorf("the call's request carried %v", call.Header)
	}
	for args, want := range map[string]string{
		`{"where":{}}`:                         "get_item: id is required",
		`{"id":"x","where":{"a":[1]}}`:         "get_item: where.a is a value, for equality, or an object of operators",
		`{"id":"x","where":{"a":{"in":[""]}}}`: "get_item: where.a.in[0] must not be blank or hold a comma or a space at either end",
		`{"id":"x","idempotencyKey":7}`:        "get_item: idempotencyKey is 1 to 255 printable ASCII characters",
	} {
		if p := structured(it.call(t, "ada", "get_item", args)); p["detail"] != want {
			t.Errorf("get_item %s answered %v, want %s", args, p, want)
		}
	}
}

func TestAGateHoldsACallAndAServedToolIsOffered(t *testing.T) {
	var gated []string
	it := newItems(t, EndpointOptions{
		ConfirmationKey: testKey,
		CallGate: CallGateFunc(func(ctx context.Context, tool RouteTool, args map[string]any) (json.RawMessage, error) {
			gated = append(gated, tool.Name+" "+CanonicalJSON(args)+" "+fmt.Sprint(ToolCallOf(ctx) == nil))
			if tool.Name == "get_item" && args["id"] == "held" {
				return marshal(map[string]any{"type": "about:blank", "title": "approval_required", "status": 428, "code": "approval_required"}), nil
			}
			return nil, nil
		}),
		Tools: []ServedTool{{
			Tool: Tool{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Call: func(ctx context.Context, args map[string]any) (any, *ServedProblem, error) {
				if args["q"] == "" {
					return nil, &ServedProblem{Status: 400, Title: "Bad Request", Detail: "q is required"}, nil
				}
				return map[string]any{"as": session.GetPrincipalID(ctx), "limit": args["limit"]}, nil, nil
			},
		}},
	})
	if p := structured(it.call(t, "ada", "get_item", `{"id":"held"}`)); p["code"] != "approval_required" || len(it.calls) != 0 {
		t.Errorf("a held call answered %v and reached the route %d times", p, len(it.calls))
	}
	it.call(t, "ada", "get_item", `{"id":"free"}`)
	if len(it.calls) != 1 || len(gated) != 2 || gated[1] != `get_item {"id":"free"} true` {
		t.Errorf("the gate heard %v", gated)
	}
	_, listed := it.rpc(t, "ada", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false)
	if tools := listed["result"].(map[string]any)["tools"].([]any); tools[len(tools)-1].(map[string]any)["name"] != "search" {
		t.Errorf("the served tool is not listed: %v", tools)
	}
	if got := string(marshal(structured(it.call(t, "ada", "search", `{"q":"desk","limit":5}`)))); got != `{"as":"ada","limit":5}` {
		t.Errorf("search answered %s", got)
	}
	if p := structured(it.call(t, "ada", "search", `{"q":""}`)); p["status"] != float64(400) || p["detail"] != "q is required" {
		t.Errorf("a refused search answered %v", p)
	}
	tools, _ := ParseRouteTools(itemTools)
	if _, err := (Mount{API: "items-api", Tools: tools, Routes: it.mux}).Handler(EndpointOptions{}); err == nil || !strings.Contains(err.Error(), "ConfirmationKey is required") {
		t.Errorf("a tool that confirms with no key mounted: %v", err)
	}
	if _, err := (Mount{API: "items-api", Tools: tools, Routes: it.mux}).Handler(EndpointOptions{ConfirmationKey: testKey, Tools: []ServedTool{{Tool: Tool{Name: "purge"}}}}); err == nil {
		t.Error("a served tool named as a route's tool mounted")
	}
}
