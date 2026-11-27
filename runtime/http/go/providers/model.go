package providers

import (
	"context"
	"encoding/json"
)

// The model provider (D159): completes one turn of an agent run (D158)
// with the tools the run's role allows, and reports what the turn used.
// Content from outside, a tool result that carries an inbound message, a
// web page or a document, is marked untrusted, and a provider passes the
// mark on in the way its API takes it. No credential value is ever in a
// message: a credential reaches only the runner (credentials.go).

// The roles of a model message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ModelMessage is one message of a turn. An assistant message is an
// earlier completion of the run, replayed: its content, the tool calls it
// made, which the tool messages after it answer, and the provider's own
// state of it, as the provider returned them. A provider builds its API's
// conversation from these alone.
type ModelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCallID is, for a tool message, the call it answers.
	ToolCallID string `json:"toolCallId,omitempty"`
	// ToolCalls are, for an assistant message, the tool calls its
	// completion made, in order.
	ToolCalls []ModelToolCall `json:"toolCalls,omitempty"`
	// ProviderState is, for an assistant message, the ProviderState its
	// completion returned, unchanged.
	ProviderState json.RawMessage `json:"providerState,omitempty"`
	// Untrusted marks content from outside the app, which is data and
	// never instructions.
	Untrusted bool `json:"untrusted,omitempty"`
}

// ModelTool is a tool the model may call.
type ModelTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is the JSON Schema of its input.
	InputSchema map[string]any `json:"inputSchema"`
}

// ModelTurn is a turn to complete.
type ModelTurn struct {
	// Model is the model, by the provider's name for it; the provider's
	// default when empty.
	Model     string         `json:"model,omitempty"`
	Messages  []ModelMessage `json:"messages"`
	Tools     []ModelTool    `json:"tools,omitempty"`
	MaxTokens int            `json:"maxTokens,omitempty"`
}

// ModelToolCall is a tool call the model made.
type ModelToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ModelUsage is what a turn used, for the Budget behavior (D104).
type ModelUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	// Amount is what the provider charges, when it says.
	Amount *Amount `json:"amount,omitempty"`
}

// The stops of a completion.
const (
	StopEnd       = "end"
	StopToolUse   = "toolUse"
	StopMaxTokens = "maxTokens"
)

// ModelCompletion is a completed turn.
type ModelCompletion struct {
	Content   string          `json:"content"`
	ToolCalls []ModelToolCall `json:"toolCalls"`
	Stop      string          `json:"stop"`
	Usage     ModelUsage      `json:"usage"`
	// ProviderState is the provider's own record of the turn, as JSON,
	// which no one but the provider reads; the dispatcher passes it back,
	// unchanged, on that assistant message in every later turn. A provider
	// that keeps none leaves it nil.
	ProviderState json.RawMessage `json:"providerState,omitempty"`
}

// ModelEmbedRequest is texts to embed (D155): each becomes one vector,
// under Model or the provider's default.
type ModelEmbedRequest struct {
	Model string   `json:"model,omitempty"`
	Texts []string `json:"texts"`
}

// ModelEmbedding is the vectors of an embed request, one per text in
// order, and what it used.
type ModelEmbedding struct {
	Vectors [][]float64 `json:"vectors"`
	Usage   *ModelUsage `json:"usage,omitempty"`
}

// ModelProvider is a model provider.
type ModelProvider interface {
	Complete(ctx context.Context, turn ModelTurn) (ModelCompletion, error)
}

// ModelEmbedder is a model provider's optional embed, for search's
// re-ranking (D155). It is no operation of the model contract, as it is
// none of TypeScript's: a provider that embeds nothing leaves it out, and
// search runs on full text alone.
type ModelEmbedder interface {
	Embed(ctx context.Context, request ModelEmbedRequest) (ModelEmbedding, error)
}
