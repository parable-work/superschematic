package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var noteSchema = json.RawMessage(`{
	"kind": "General",
	"name": "Note",
	"types": {"Note": {"name": "Note", "role": "EmbeddedStruct", "fields": [
		{"name": "body", "typeRef": {"name": "string"}, "required": true}
	]}}
}`)

var alice = &Principal{Subject: "alice"}

func openTest(t *testing.T, cfg Config) *Engine {
	t.Helper()
	if cfg.Path == "" && cfg.Postgres == nil {
		cfg.Path = filepath.Join(t.TempDir(), "engine.db")
	}
	if cfg.Policy == nil {
		cfg.Policy = AllowAll()
	}
	e, err := Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestAnEngineOpensWithTheCoresBehaviors(t *testing.T) {
	e := openTest(t, Config{})
	info := e.Info()
	if info.ABI != ABIVersion || info.Dialect != "sqlite" {
		t.Fatalf("info = %+v", info)
	}
	for _, name := range []string{"Workflow", "Comments", "Timers"} {
		found := false
		for _, behavior := range info.Behaviors {
			found = found || behavior == name
		}
		if !found {
			t.Errorf("%s is not registered: %v", name, info.Behaviors)
		}
	}
}

func TestAConfigTheEngineCannotTakeIsATypeError(t *testing.T) {
	_, err := Open(Config{Path: filepath.Join(t.TempDir(), "engine.db")})
	var refused *Error
	if !errors.As(err, &refused) || refused.Class != "TypeError" || !strings.Contains(refused.Message, "access policy") {
		t.Fatalf("open without a policy: %v", err)
	}
	_, err = OpenJSON([]byte(`{"path": "x.db", "policy": "allowAll", "polcy": 1}`), 0)
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, `"polcy"`) {
		t.Fatalf("open with a misspelt option: %v", err)
	}
}

func TestACallReturnsItsResultAndARefusalItsCodeAndDetails(t *testing.T) {
	ctx := context.Background()
	e := openTest(t, Config{})
	if _, err := e.Call(ctx, "schemas.define", alice, noteSchema); err != nil {
		t.Fatal(err)
	}
	published, err := e.Call(ctx, "schemas.publish", alice, "Note")
	if err != nil || !strings.Contains(string(published), `"version":1`) {
		t.Fatalf("publish = %s, %v", published, err)
	}
	created, err := e.Call(ctx, "instances.create", alice, "Note", map[string]any{"body": "hi"}, map[string]any{"id": "n1"})
	if err != nil || !strings.Contains(string(created), `"id":"n1"`) {
		t.Fatalf("create = %s, %v", created, err)
	}
	missing, err := e.Call(ctx, "instances.get", alice, "Note", "n9")
	if err != nil || missing != nil {
		t.Fatalf("a get of nothing = %s, %v", missing, err)
	}
	_, err = e.Call(ctx, "instances.create", alice, "Note", map[string]any{"body": 7})
	var refused *Error
	if !errors.As(err, &refused) {
		t.Fatalf("an invalid instance: %v", err)
	}
	if refused.Class != "InstanceValidationError" || refused.Code != CodeInvalidInstance || refused.Status() != 422 {
		t.Fatalf("refused = %+v", refused)
	}
	if !strings.Contains(string(refused.Details), `"issues"`) {
		t.Fatalf("details = %s", refused.Details)
	}
	if !IsCode(err, CodeInvalidInstance) || IsCode(err, CodeNotFound) {
		t.Fatal("IsCode")
	}
	_, err = e.Call(ctx, "instances.frobnicate", alice)
	if !errors.As(err, &refused) || refused.Class != "TypeError" || refused.Refused() {
		t.Fatalf("an unknown call: %v", err)
	}
}

func TestDeployPublishesOnceAndLeavesALiveVersionAsItIs(t *testing.T) {
	ctx := context.Background()
	e := openTest(t, Config{})
	first, err := e.Deploy(ctx, []json.RawMessage{noteSchema}, DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := Deployed{Name: "Note", Namespace: "default", Version: 1, Defined: true, Published: true}
	if len(first) != 1 || first[0] != want {
		t.Fatalf("first deploy = %+v", first)
	}
	again, err := e.Deploy(ctx, []json.RawMessage{noteSchema}, DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want.Defined, want.Published = false, false
	if len(again) != 1 || again[0] != want {
		t.Fatalf("second deploy = %+v", again)
	}
	_, err = e.Deploy(ctx, []json.RawMessage{json.RawMessage(`{"kind": "General", "name": "Bad", "types": {}}`)}, DeployOptions{})
	if !IsCode(err, CodeInvalidSchema) {
		t.Fatalf("a bad document: %v", err)
	}
}

func TestHealthIsReadyUntilTheEngineCloses(t *testing.T) {
	ctx := context.Background()
	e := openTest(t, Config{})
	health, err := e.Health(ctx)
	if err != nil || !health.Ready || health.Dialect != "sqlite" {
		t.Fatalf("health = %+v, %v", health, err)
	}
	if err := e.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal("a second close:", err)
	}
	health, err = e.Health(ctx)
	if err != nil || health.Ready || !health.Closed {
		t.Fatalf("health after close = %+v, %v", health, err)
	}
	if !errors.Is(e.Ready(ctx), ErrClosed) {
		t.Fatal("ready after close")
	}
	if _, err := e.Call(ctx, "events.head", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("a call after close: %v", err)
	}
}

func TestTheRunnerRunsWhatIsDue(t *testing.T) {
	ctx := context.Background()
	e := openTest(t, Config{Runner: &Runner{Principal: Principal{Subject: "runner"}}})
	pass, err := e.Runner().RunDue(ctx)
	if err != nil || !strings.HasPrefix(string(pass), "{") {
		t.Fatalf("run due = %s, %v", pass, err)
	}
	if err := e.Runner().Start(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := e.Runner().Status(ctx)
	if err != nil || !strings.Contains(string(status), `"running":true`) {
		t.Fatalf("status = %s, %v", status, err)
	}
	if err := e.Runner().Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestARequestIsServedAndAStreamWokenByACommit(t *testing.T) {
	ctx := context.Background()
	e := openTest(t, Config{})
	if _, err := e.Deploy(ctx, []json.RawMessage{noteSchema}, DeployOptions{}); err != nil {
		t.Fatal(err)
	}
	caller := Caller{Principal: alice}
	created, err := e.ServeHTTPRequest(ctx, HTTPRequest{
		Method:    "POST",
		Prefix:    "/api",
		Path:      "/api/namespaces/default/schemas/Note/instances",
		Headers:   [][2]string{{"content-type", "application/json"}},
		Caller:    caller,
		RequestID: "r1",
	}, []byte(`{"id": "n1", "data": {"body": "hello"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != 201 || created.Header("ETag") != `"1"` || created.Stream != nil {
		t.Fatalf("created = %d %v", created.Status, created.Headers)
	}
	if !strings.Contains(string(created.Body), `"requestId":"r1"`) {
		t.Fatalf("body = %s", created.Body)
	}
	opened, err := e.ServeHTTPRequest(ctx, HTTPRequest{
		Method:  "GET",
		Path:    "/namespaces/default/events",
		Headers: [][2]string{{"accept", "text/event-stream"}},
		Caller:  caller,
		Options: &HTTPOptions{Stream: &StreamOptions{HeartbeatMs: 60000}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Stream == nil || opened.Header("content-type") != "text/event-stream" {
		t.Fatalf("opened = %d %v", opened.Status, opened.Headers)
	}
	stream := opened.Stream
	defer stream.Close()
	var sent strings.Builder
	for !strings.Contains(sent.String(), "event: ready") {
		data, over, err := stream.Poll(ctx)
		if err != nil || over {
			t.Fatalf("poll: %v, over %v", err, over)
		}
		sent.Write(data)
	}
	changed := e.Changed()
	if _, err := e.Call(ctx, "instances.create", alice, "Note", map[string]any{"body": "live"}, map[string]any{"id": "n2"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("the commit did not wake the stream")
	}
	data, over, err := stream.Poll(ctx)
	if err != nil || over || !strings.Contains(string(data), `"instanceId":"n2"`) {
		t.Fatalf("poll after the commit = %q, %v, %v", data, over, err)
	}
	stream.Close()
	if _, over, _ := stream.Poll(ctx); !over {
		t.Fatal("a closed stream goes on")
	}
	// A close ends every stream and wakes every waiter.
	again, err := e.ServeHTTPRequest(ctx, HTTPRequest{
		Method:  "GET",
		Path:    "/namespaces/default/events",
		Headers: [][2]string{{"accept", "text/event-stream"}},
		Caller:  caller,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed = e.Changed()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("the close did not wake the waiter")
	}
	if _, over, _ := again.Stream.Poll(ctx); !over {
		t.Fatal("a stream goes on after the close")
	}
}

func TestACallWaitsForASlotUntilItsContextEnds(t *testing.T) {
	e := openTest(t, Config{Concurrency: 1})
	e.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.Call(ctx, "events.head", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call with no slot: %v", err)
	}
	<-e.slots
	if _, err := e.Call(context.Background(), "events.head", nil); err != nil {
		t.Fatal(err)
	}
}
