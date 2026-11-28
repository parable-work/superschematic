package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/runtime/http/go/idempotency"
	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// A generated router's tools (D81, D116): a tool of the table the build
// writes, the request a call of it makes to its operation's route, and
// what the route's response comes to, the port of the TypeScript
// runtime's McpRouteTool, toolRequest, filterQuery, outcomeOf and
// problemOf and the Rust runtime's mcp::route.

// IdempotencyArgument is the argument the tool of an @idempotent operation
// takes the call's key in (D140): a call has no headers, so the endpoint
// sends the argument to the route as its Idempotency-Key.
const IdempotencyArgument = "idempotencyKey"

// ToolArgument is where one tool argument goes in the request to its
// operation's route: In is path, query (once per element of a list), body
// (a member of the JSON body object) or filter (the operation's FilterBy,
// D106, an object with a member per field, which goes to the query string
// as the bracket form under Name).
type ToolArgument struct {
	Key  string `json:"key"`
	In   string `json:"in"`
	Name string `json:"name"`
}

// ToolAutonomy is an operation's @autonomy as its tool's record carries
// it (D158, D183): its class and the mode a policy row may override.
type ToolAutonomy struct {
	Class string `json:"class"`
	Mode  string `json:"mode,omitempty"`
}

// ToolIdempotency is an @idempotent operation's tool (D140): whether a
// call must send a key.
type ToolIdempotency struct {
	Required bool `json:"required,omitempty"`
}

// RouteTool is one tool of a generated router: what tools/list answers,
// and the operation whose route a call runs through. It reads the JSON the
// build writes a tool as (mcptools), the TypeScript runtime's
// McpRouteTool.
type RouteTool struct {
	Tool
	// Operation is the operation's key, <namespace>.<name>.
	Operation string `json:"operation"`
	// Method and Path are its route's, the path with a {name} for each
	// path parameter, as the router mounts it.
	Method string `json:"method"`
	Path   string `json:"path"`
	// Arguments are where each argument goes in the route's request.
	Arguments []ToolArgument `json:"arguments"`
	// Confirm is true for a tool whose call runs only once the person
	// confirms it (D93).
	Confirm bool `json:"confirm,omitempty"`
	// Idempotent is the operation's @idempotent (D140).
	Idempotent *ToolIdempotency `json:"idempotent,omitempty"`
	// Autonomy is the operation's @autonomy (D158, D183), which an
	// endpoint's call gate holds an agent run's call to.
	Autonomy *ToolAutonomy `json:"autonomy,omitempty"`
}

// ParseRouteTools reads a tool table the build wrote.
func ParseRouteTools(table string) ([]RouteTool, error) {
	var tools []RouteTool
	if err := json.Unmarshal([]byte(table), &tools); err != nil {
		return nil, fmt.Errorf("mcp: the tool table: %w", err)
	}
	return tools, nil
}

// Idempotency is the operation's @idempotent when the tool takes the key
// argument: the operation is @idempotent, and no argument of its own has
// the name, which then stays the operation's.
func (t RouteTool) Idempotency() *ToolIdempotency {
	if t.Idempotent == nil || slices.ContainsFunc(t.Arguments, func(a ToolArgument) bool { return a.Key == IdempotencyArgument }) {
		return nil
	}
	return t.Idempotent
}

// Listed is the tool as tools/list answers it: an @idempotent operation's
// with the argument its key goes in, and a tool that asks with the
// argument its confirmation comes back in.
func (t RouteTool) Listed() Tool {
	tool := t.Tool
	if idempotent := t.Idempotency(); idempotent != nil {
		tool.InputSchema = WithIdempotencyArgument(tool.InputSchema, idempotent.Required)
	}
	if t.Confirm {
		tool.InputSchema = WithConfirmationArgument(tool.InputSchema)
	}
	return tool
}

// withProperty is schema with the property name added, and required when
// required is set; schema itself when it is no object.
func withProperty(schema json.RawMessage, name string, property map[string]any, required bool) json.RawMessage {
	var object map[string]any
	if err := json.Unmarshal(schema, &object); err != nil || object == nil {
		return schema
	}
	properties, _ := object["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties[name] = property
	object["properties"] = properties
	if required {
		listed, _ := object["required"].([]any)
		object["required"] = append(listed, name)
	}
	return marshal(object)
}

// WithIdempotencyArgument is the input schema of an @idempotent
// operation's tool (D140), with the argument its key goes in, required
// when the operation requires one.
func WithIdempotencyArgument(schema json.RawMessage, required bool) json.RawMessage {
	return withProperty(schema, IdempotencyArgument, map[string]any{
		"type":        "string",
		"minLength":   1,
		"maxLength":   255,
		"description": "A key you make for this call, a UUID say. Send the same key with the same arguments to repeat the call safely: it runs once, and the repeat is answered what the first call was. Use a new key for a new call.",
	}, required)
}

// WithConfirmationArgument is the input schema of a tool that confirms
// (D93), with the optional argument a confirmation token comes back in.
func WithConfirmationArgument(schema json.RawMessage) json.RawMessage {
	return withProperty(schema, ConfirmationArgument, map[string]any{
		"type":        "string",
		"description": "The confirmation token a call of this tool answered with confirmation_required, sent once the person confirms the call; leave it out to ask for one.",
	}, false)
}

// unforwarded are the headers of the endpoint's request a call's request
// does not carry: its own body's, its conditions, the MCP transport's and
// the connection's. Every other one goes on, so an authenticator that
// reads its own header authenticates the call as it did the endpoint.
var unforwarded = map[string]bool{
	"accept": true, "idempotency-key": true, "connection": true, "content-encoding": true,
	"content-length": true, "content-type": true, "expect": true, "host": true, "if-match": true,
	"if-modified-since": true, "if-none-match": true, "if-range": true, "if-unmodified-since": true,
	"keep-alive": true, "last-event-id": true, "mcp-method": true, "mcp-name": true,
	"mcp-protocol-version": true, "mcp-session-id": true, "range": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

// wireText is a path or query value as the route reads it.
func wireText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return numberText(v)
	case float64:
		return JSNumber(v)
	case bool:
		return strconv.FormatBool(v)
	}
	var buf bytes.Buffer
	writeCanonicalOrdered(&buf, value)
	return buf.String()
}

// writeCanonicalOrdered writes value as JSON.stringify would, its members
// in the order a map gives them sorted, which is all a Go map has.
func writeCanonicalOrdered(buf *bytes.Buffer, value any) {
	writeCanonical(buf, plainValue(value))
}

func isWireValue(value any) bool {
	switch value.(type) {
	case string, json.Number, float64, bool:
		return true
	}
	return false
}

// maxFilterIn is the most values a filter's in or notIn may list, the
// routes' reader's limit.
const maxFilterIn = 100

func isListOperator(operator string) bool { return operator == "in" || operator == "notIn" }

// outerSpace reports whether s starts or ends with a space the routes'
// reader trims an in's values of: JavaScript's \s, and U+0085.
func outerSpace(s string) bool {
	space := func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF || r == 0x85 }
	first, last := []rune(s)[0], []rune(s)[len([]rune(s))-1]
	return space(first) || space(last)
}

// filterQuery writes a tool's filter argument (FilterBy, D106) into query
// as the bracket form the route's reader reads, under name, the parameter
// the brackets follow; it returns what is wrong with a filter that has no
// bracket form, after the argument's name, as the SDKs refuse it.
func filterQuery(query *[]string, name string, filter any) string {
	object, ok := filter.(map[string]any)
	if !ok {
		return " is an object with a member per field"
	}
	for _, field := range slices.Sorted(mapKeys(object)) {
		member := object[field]
		if isWireValue(member) {
			*query = append(*query, url.QueryEscape(name+"["+field+"]")+"="+url.QueryEscape(wireText(member)))
			continue
		}
		operators, ok := member.(map[string]any)
		if !ok {
			return "." + field + " is a value, for equality, or an object of operators"
		}
		for _, operator := range slices.Sorted(mapKeys(operators)) {
			operand := operators[operator]
			key := name + "[" + field + "][" + operator + "]"
			at := "." + field + "." + operator
			list, isList := operand.([]any)
			allWire := isList && !slices.ContainsFunc(list, func(item any) bool { return !isWireValue(item) })
			switch {
			case isListOperator(operator) && (isWireValue(operand) || allWire):
				items := list
				if !isList {
					items = []any{operand}
				}
				if len(items) > maxFilterIn {
					return fmt.Sprintf("%s holds more than %d values", at, maxFilterIn)
				}
				texts := make([]string, len(items))
				for i, item := range items {
					text := wireText(item)
					if text == "" || strings.Contains(text, ",") || outerSpace(text) {
						return fmt.Sprintf("%s[%d] must not be blank or hold a comma or a space at either end", at, i)
					}
					texts[i] = text
				}
				*query = append(*query, url.QueryEscape(key)+"="+url.QueryEscape(strings.Join(texts, ",")))
			case operator == "contains" && operand == "":
				return at + " must not be empty"
			case !isListOperator(operator) && isWireValue(operand):
				*query = append(*query, url.QueryEscape(key)+"="+url.QueryEscape(wireText(operand)))
			case isListOperator(operator):
				return at + " is a list of values"
			default:
				return at + " is a value"
			}
		}
	}
	return ""
}

func mapKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}

// RequestRefusal is a call's refusal before any request is made: its
// status, detail and code.
type RequestRefusal struct {
	Status int
	Detail string
	Code   string
}

// ToolRequest is the request to the tool's route that a call with args
// makes, carrying the endpoint request's credentials and context, or the
// refusal of args that name an argument the tool does not take, leave out
// a path parameter, or hold a filter with no bracket form.
func ToolRequest(ctx context.Context, tool RouteTool, args map[string]any, endpoint *http.Request) (*http.Request, *RequestRefusal) {
	keys := make([]string, 0, len(tool.Arguments)+1)
	for _, argument := range tool.Arguments {
		keys = append(keys, argument.Key)
	}
	// An @idempotent operation's tool takes the call's key as an argument,
	// which goes to the route as the header a request carries it in (D140).
	idempotencyKey := ""
	if tool.Idempotency() != nil {
		keys = append(keys, IdempotencyArgument)
		if key, ok := args[IdempotencyArgument]; ok && key != nil {
			text, isText := key.(string)
			if !isText || !idempotency.IsKey(text) {
				return nil, &RequestRefusal{Status: http.StatusBadRequest, Detail: tool.Name + ": " + IdempotencyArgument + " is 1 to 255 printable ASCII characters", Code: idempotency.CodeInvalid}
			}
			idempotencyKey = text
		}
	}
	for _, key := range slices.Sorted(mapKeys(args)) {
		if !slices.Contains(keys, key) {
			takes := "takes no arguments"
			if len(keys) > 0 {
				takes = "takes " + strings.Join(keys, ", ")
			}
			return nil, &RequestRefusal{Status: http.StatusBadRequest, Detail: tool.Name + " " + takes + ", not " + string(marshal(key)), Code: "BAD_REQUEST"}
		}
	}
	path := tool.Path
	var query []string
	body := map[string]any{}
	hasBody := false
	for _, argument := range tool.Arguments {
		value, present := args[argument.Key]
		switch argument.In {
		case "path":
			if !present || value == nil {
				return nil, &RequestRefusal{Status: http.StatusBadRequest, Detail: tool.Name + ": " + argument.Key + " is required", Code: "BAD_REQUEST"}
			}
			path = strings.ReplaceAll(path, "{"+argument.Name+"}", encodeURIComponent(wireText(value)))
		case "query":
			if !present || value == nil {
				continue
			}
			items, isList := value.([]any)
			if !isList {
				items = []any{value}
			}
			for _, item := range items {
				query = append(query, url.QueryEscape(argument.Name)+"="+url.QueryEscape(wireText(item)))
			}
		case "filter":
			if !present || value == nil {
				continue
			}
			if wrong := filterQuery(&query, argument.Name, value); wrong != "" {
				return nil, &RequestRefusal{Status: http.StatusBadRequest, Detail: tool.Name + ": " + argument.Key + wrong, Code: "BAD_REQUEST"}
			}
		case "body":
			if !present {
				continue
			}
			body[argument.Name] = value
			hasBody = true
		}
	}
	target := &url.URL{Path: decodedPath(path), RawPath: path, RawQuery: strings.Join(query, "&")}
	var reader io.Reader = http.NoBody
	var content []byte
	if hasBody {
		content = marshal(body)
		reader = bytes.NewReader(content)
	}
	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(tool.Method), target.String(), reader)
	if err != nil {
		return nil, &RequestRefusal{Status: http.StatusBadRequest, Detail: tool.Name + ": " + err.Error(), Code: "BAD_REQUEST"}
	}
	request.URL = target
	request.RequestURI = ""
	request.RemoteAddr = endpoint.RemoteAddr
	request.Host = endpoint.Host
	request.TLS = endpoint.TLS
	for name, values := range endpoint.Header {
		if unforwarded[strings.ToLower(name)] {
			continue
		}
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Accept", "application/json")
	if hasBody {
		request.Header.Set("Content-Type", "application/json")
		request.ContentLength = int64(len(content))
	}
	if idempotencyKey != "" {
		request.Header.Set(idempotency.KeyHeader, idempotencyKey)
	}
	return request, nil
}

// decodedPath is path with its percent-encoding read, or path itself when
// it holds none that reads.
func decodedPath(path string) string {
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return path
	}
	return decoded
}

// encodeURIComponent is JavaScript's: every byte but the unreserved marks
// percent-encoded.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func mediaType(header http.Header) string {
	media, _, _ := strings.Cut(header.Get("Content-Type"), ";")
	return strings.ToLower(strings.TrimSpace(media))
}

func isJSONMedia(media string) bool {
	return media == "application/json" || strings.HasSuffix(media, "+json")
}

func isTextMedia(media string) bool {
	return media == "" || strings.HasPrefix(media, "text/") || media == "application/xml" || strings.HasSuffix(media, "+xml")
}

// ProblemOf is a route's refusal (4xx or 5xx) as its problem document, or
// one made of its status and text when the route answered another body.
func ProblemOf(status int, header http.Header, body []byte, requestID string) json.RawMessage {
	if isJSONMedia(mediaType(header)) {
		if value, err := decodeJSON(body); err == nil {
			if _, isObject := value.(map[string]any); isObject {
				return json.RawMessage(compact(body))
			}
		}
	}
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = "Request failed"
	}
	return marshal(Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, RequestID: requestID})
}

// OutcomeOf is what the route's response comes to: a refusal as its
// problem document (ProblemOf); a success's envelope data, a JSON body that
// is no envelope as it is, text as text content, and any other body as a
// resource at uri, the route's path.
func OutcomeOf(status int, header http.Header, body []byte, requestID, uri string) CallOutcome {
	if status >= 400 {
		return CallOutcome{Problem: ProblemOf(status, header, body, requestID)}
	}
	if len(body) == 0 {
		return CallOutcome{Result: json.RawMessage("null")}
	}
	media := mediaType(header)
	if isJSONMedia(media) {
		var envelope struct {
			Data json.RawMessage `json:"data"`
			Meta json.RawMessage `json:"meta"`
		}
		value, err := decodeJSON(body)
		if err != nil {
			return CallOutcome{Content: []Content{{Type: "text", Text: string(body)}}}
		}
		if object, isObject := value.(map[string]any); isObject {
			_, hasData := object["data"]
			_, metaObject := object["meta"].(map[string]any)
			if hasData && metaObject && json.Unmarshal(body, &envelope) == nil {
				return CallOutcome{Result: envelope.Data}
			}
		}
		return CallOutcome{Result: json.RawMessage(compact(body))}
	}
	if isTextMedia(media) {
		return CallOutcome{Content: []Content{{Type: "text", Text: string(body)}}}
	}
	return CallOutcome{Content: []Content{{Type: "resource", Resource: &ContentResource{URI: uri, MimeType: media, Blob: base64.StdEncoding.EncodeToString(body)}}}}
}

// CallerOf is who is calling, which a confirmation is bound to (D93): the
// principal's id and the calling service's deployable the auth steps
// established, as JSON, null for each they did not, as the TypeScript and
// Rust runtimes write it.
func CallerOf(ctx context.Context) string {
	var subject, deployable any
	if id := session.GetPrincipalID(ctx); id != "" {
		subject = id
	}
	if caller, ok := serviceauth.CallerFromContext(ctx); ok && caller != nil && caller.Deployable != "" {
		deployable = caller.Deployable
	}
	return string(marshal([]any{subject, deployable}))
}
