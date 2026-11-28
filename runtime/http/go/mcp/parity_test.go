package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The engine's MCP corpus (runtime/engine/testdata/http/mcp.json), which
// the engine's tests record through the official MCP client, replayed
// through this runtime's transport (D81, D116, #660), as the TypeScript
// runtime's mcp-parity.test.ts and the Rust runtime's tests/mcp_parity.rs
// replay it. The tools differ, so each exchange's tools answer what the
// engine's answered, and the transport must frame them as the engine did:
// the statuses, the headers, the event stream of the 2025 revisions and
// the single JSON body of 2026-07-28, the ids, initialize and
// server/discover, the 202 of a notification and the 405 of a GET, a
// result's text and structured content, a tool error carrying its
// problem, and an unknown tool's invalid-params error. A call's text is
// the engine's byte for byte. Exchanges the transport does not answer, the
// gate's 401 and the engine's 404 for a namespace it has not got, are left
// out.

const corpusPath = "../../../engine/testdata/http/mcp.json"

var recordedHeaders = []string{"cache-control", "content-type", "mcp-session-id"}

type corpusFile struct {
	Shared map[string]any `json:"shared"`
	Cases  []struct {
		Name  string           `json:"name"`
		Steps []map[string]any `json:"steps"`
	} `json:"cases"`
}

func resolveShared(value any, shared map[string]any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = resolveShared(item, shared)
		}
		return out
	case map[string]any:
		if name, ok := v["$shared"].(string); ok {
			return resolveShared(shared[name], shared)
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = resolveShared(item, shared)
		}
		return out
	}
	return value
}

// replayTools answer what the engine answered the recorded reply.
type replayTools struct{ reply map[string]any }

func (t replayTools) List(context.Context) ([]Tool, error) {
	result, _ := t.reply["result"].(map[string]any)
	listed, _ := result["tools"].([]any)
	var tools []Tool
	if err := json.Unmarshal(marshal(listed), &tools); err != nil {
		return nil, err
	}
	return tools, nil
}

func (t replayTools) Call(context.Context, string, map[string]any) (CallOutcome, error) {
	if failure, ok := t.reply["error"].(map[string]any); ok {
		message, _ := failure["message"].(string)
		if fmt.Sprint(failure["code"]) == "-32602" {
			return CallOutcome{}, &UnknownToolError{Message: message}
		}
		return CallOutcome{}, errors.New(message)
	}
	result, _ := t.reply["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if result["isError"] == true {
		// The problem as the engine wrote it, its members in their order.
		if len(content) == 1 {
			if text, ok := objectOf(content[0])["text"].(string); ok && json.Valid([]byte(text)) {
				return CallOutcome{Problem: json.RawMessage(text)}, nil
			}
		}
		return CallOutcome{Problem: marshal(result["structuredContent"])}, nil
	}
	if len(content) == 1 {
		item, _ := content[0].(map[string]any)
		if text, ok := item["text"].(string); ok && item["type"] == "text" {
			if json.Valid([]byte(text)) {
				return CallOutcome{Result: json.RawMessage(text)}, nil
			}
			// A value JSON cannot hold, a time, is a symbol in the text,
			// and a string in the structured content.
			if structured, ok := result["structuredContent"]; ok {
				return CallOutcome{Result: marshal(structured)}, nil
			}
			return CallOutcome{Result: marshal(text)}, nil
		}
	}
	var items []Content
	if err := json.Unmarshal(marshal(content), &items); err != nil {
		return CallOutcome{}, err
	}
	return CallOutcome{Content: items}, nil
}

func (replayTools) Confirms(context.Context, string) (*Tool, error) { return nil, nil }

func (replayTools) Check(context.Context, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}

// parseFrames are the frames of an event-stream body, as the corpus
// records them.
func parseFrames(t *testing.T, text string) []any {
	var frames []any
	for _, block := range strings.Split(text, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		frame := map[string]any{}
		for _, line := range strings.Split(block, "\n") {
			field, value, ok := strings.Cut(line, ": ")
			if !ok {
				t.Fatalf("a frame line with no field: %q", line)
			}
			if field == "data" {
				parsed, err := decodeJSON([]byte(value))
				if err != nil {
					t.Fatalf("a frame's data is not JSON: %v", err)
				}
				frame[field] = parsed
			} else {
				frame[field] = value
			}
		}
		frames = append(frames, frame)
	}
	return frames
}

// comparable is value with the text of each text content item that holds
// a symbol ({{time:1}} where the engine wrote a time) replaced by one
// mark, since the corpus writes it bare in the text and quoted in the
// structured content, which is compared instead; numbers as float64.
func comparable(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = comparable(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if text, ok := item.(string); ok && key == "text" && v["type"] == "text" && strings.Contains(text, "{{") {
				out[key] = "<symbol>"
				continue
			}
			out[key] = comparable(item)
		}
		return out
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return value
}

// texts is the text of each tool result's content in a reply.
func texts(reply any) []string {
	result, _ := objectOf(reply)["result"].(map[string]any)
	content, _ := result["content"].([]any)
	var out []string
	for _, item := range content {
		if object := objectOf(item); object["type"] == "text" {
			text, _ := object["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

type replay struct {
	name     string
	exchange map[string]any
	frames   []any
	server   Options
}

func loadReplays(t *testing.T) []replay {
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read the corpus: %v", err)
	}
	var corpus corpusFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatalf("parse the corpus: %v", err)
	}
	engine := Options{ServerInfo: ServerInfo{Name: "@superschematic/engine", Version: "0.0.0"}}
	var replays []replay
	for _, c := range corpus.Cases {
		server := engine
		for index, step := range c.Steps {
			if _, isApp := step["app"]; isApp {
				mounts, _ := step["mounts"].([]any)
				if mounts == nil {
					mounts = []any{map[string]any{"kind": step["kind"], "options": step["options"]}}
				}
				server = engine
				for _, mount := range mounts {
					m := objectOf(mount)
					if m["kind"] != "mcp" {
						continue
					}
					options := objectOf(m["options"])
					if info, ok := options["serverInfo"].(map[string]any); ok {
						server.ServerInfo = ServerInfo{Name: fmt.Sprint(info["name"]), Version: fmt.Sprint(info["version"])}
					}
					if instructions, ok := options["instructions"].(string); ok {
						server.Instructions = instructions
					}
				}
				continue
			}
			if _, isRequest := step["request"]; !isRequest {
				continue
			}
			exchange := objectOf(resolveShared(step, corpus.Shared))
			request, response := objectOf(exchange["request"]), objectOf(exchange["response"])
			path, _ := request["path"].(string)
			status := fmt.Sprint(response["status"])
			if !strings.HasSuffix(path, "/mcp") || status == "401" || status == "404" {
				continue
			}
			var frames []any
			if stream, ok := exchange["stream"].(string); ok {
				for _, later := range c.Steps[index+1:] {
					if later["stream"] != stream {
						continue
					}
					if _, isRequest := later["request"]; isRequest {
						continue
					}
					if later["end"] == true {
						break
					}
					sent, _ := resolveShared(later["frames"], corpus.Shared).([]any)
					frames = append(frames, sent...)
				}
			}
			method, _ := objectOf(request["body"])["method"].(string)
			replays = append(replays, replay{name: fmt.Sprintf("%s > exchange %d: %s %s", c.Name, index, request["method"], method), exchange: exchange, frames: frames, server: server})
		}
	}
	return replays
}

func TestTheEngineMCPCorpusThroughServe(t *testing.T) {
	replays := loadReplays(t)
	if len(replays) < 50 {
		t.Fatalf("the corpus has %d exchanges the transport answers, want more than 50", len(replays))
	}
	for _, kind := range []string{"initialize", "notifications/initialized", "tools/list", "tools/call", "server/discover"} {
		found := false
		for _, r := range replays {
			if objectOf(objectOf(r.exchange["request"])["body"])["method"] == kind {
				found = true
			}
		}
		if !found {
			t.Errorf("the corpus has no %s exchange", kind)
		}
	}
	for _, r := range replays {
		t.Run(r.name, func(t *testing.T) {
			request, response := objectOf(r.exchange["request"]), objectOf(r.exchange["response"])
			target := "http://engine.test" + fmt.Sprint(request["path"])
			if query, ok := request["query"].(string); ok && query != "" {
				target += "?" + query
			}
			var body *strings.Reader
			if sent, ok := request["body"]; ok {
				body = strings.NewReader(string(marshal(sent)))
			} else {
				body = strings.NewReader("")
			}
			sent := httptest.NewRequest(fmt.Sprint(request["method"]), target, body)
			for name, value := range objectOf(request["headers"]) {
				sent.Header.Set(name, fmt.Sprint(value))
			}
			var reply any
			if _, streamed := r.exchange["stream"]; streamed {
				if len(r.frames) > 0 {
					reply = objectOf(r.frames[0])["data"]
				}
			} else {
				reply = response["body"]
			}
			w := httptest.NewRecorder()
			Serve(w, sent, replayTools{reply: objectOf(reply)}, r.server)
			if got, want := fmt.Sprint(w.Code), fmt.Sprint(response["status"]); got != want {
				t.Fatalf("status = %s, want %s; body %s", got, want, w.Body.String())
			}
			recorded := objectOf(response["headers"])
			for _, name := range recordedHeaders {
				got := w.Header().Get(name)
				want, _ := recorded[name].(string)
				if got != want {
					t.Errorf("header %s = %q, want %q", name, got, want)
				}
			}
			text := w.Body.String()
			switch {
			case r.exchange["stream"] != nil:
				frames := comparable(parseFrames(t, text))
				want := comparable(r.frames)
				if !reflect.DeepEqual(frames, want) {
					t.Errorf("frames = %s\nwant %s", marshal(frames), marshal(want))
				}
				var gotTexts, wantTexts []string
				for _, frame := range frames.([]any) {
					gotTexts = append(gotTexts, texts(objectOf(frame)["data"])...)
				}
				for _, frame := range want.([]any) {
					wantTexts = append(wantTexts, texts(objectOf(frame)["data"])...)
				}
				if !reflect.DeepEqual(gotTexts, wantTexts) {
					t.Errorf("texts = %q, want %q", gotTexts, wantTexts)
				}
			case response["body"] != nil:
				got, err := decodeJSON([]byte(text))
				if err != nil {
					t.Fatalf("the body is not JSON: %v: %s", err, text)
				}
				if !reflect.DeepEqual(comparable(got), comparable(response["body"])) {
					t.Errorf("body = %s\nwant %s", text, marshal(response["body"]))
				}
				if g, w := texts(comparable(got)), texts(comparable(response["body"])); !reflect.DeepEqual(g, w) {
					t.Errorf("texts = %q, want %q", g, w)
				}
			default:
				if text != "" {
					t.Errorf("body = %q, want none", text)
				}
			}
		})
	}
}

var _ http.Handler = http.HandlerFunc(nil)
