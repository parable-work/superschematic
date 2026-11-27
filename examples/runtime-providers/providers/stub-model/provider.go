// Package stubmodel is stub-model in Go (D159, D184): a model provider
// whose answers follow from the turn, so a test of an agent run knows what
// each turn does. The last user or tool message decides: "call <tool>
// <json>" calls that tool when the turn offers it, and anything else is
// answered "ok: <text>". Usage counts words, one token each; content
// marked untrusted is counted and never obeyed.
package stubmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

var call = regexp.MustCompile(`(?s)^call (\S+)(?: (.*))?$`)

func words(text string) int { return len(strings.Fields(text)) }

// Definition is the provider a Go server links.
func Definition() providers.Definition {
	return providers.MustDefine(providers.Definition{
		Name:       "stub-model",
		Implements: providers.Model,
		Config: map[string]providers.SettingSpec{
			"MODEL": {Optional: true, Description: "The model name the stub reports; stub-1 when unset."},
		},
		Create: func(context.Context, providers.Settings, providers.Context) (any, error) {
			return &model{}, nil
		},
	})
}

type model struct{ calls atomic.Int64 }

func (m *model) Complete(_ context.Context, turn providers.ModelTurn) (providers.ModelCompletion, error) {
	input := 0
	for _, message := range turn.Messages {
		input += words(message.Content)
	}
	var last *providers.ModelMessage
	for i := len(turn.Messages) - 1; i >= 0; i-- {
		if r := turn.Messages[i].Role; r == providers.RoleUser || r == providers.RoleTool {
			last = &turn.Messages[i]
			break
		}
	}
	text := ""
	if last != nil {
		text = last.Content
	}
	if match := call.FindStringSubmatch(text); match != nil && !last.Untrusted && slices.ContainsFunc(turn.Tools, func(t providers.ModelTool) bool { return t.Name == match[1] }) {
		args := json.RawMessage("{}")
		if match[2] != "" {
			args = json.RawMessage(match[2])
			if !json.Valid(args) {
				return providers.ModelCompletion{}, fmt.Errorf("stub-model: the call's input is no JSON")
			}
		}
		id := fmt.Sprintf("call-%d", m.calls.Add(1))
		return providers.ModelCompletion{ToolCalls: []providers.ModelToolCall{{ID: id, Name: match[1], Input: args}}, Stop: providers.StopToolUse, Usage: providers.ModelUsage{InputTokens: input, OutputTokens: 1}}, nil
	}
	content := "ok: " + text
	output := words(content)
	stop := providers.StopEnd
	if turn.MaxTokens > 0 && turn.MaxTokens < output {
		output, stop = turn.MaxTokens, providers.StopMaxTokens
	}
	return providers.ModelCompletion{Content: content, ToolCalls: []providers.ModelToolCall{}, Stop: stop, Usage: providers.ModelUsage{InputTokens: input, OutputTokens: output}}, nil
}

// Embed is search's embeddings (D155): each text's vector is three FNV-1a
// hashes of its words, so the same text always embeds alike, as the
// TypeScript stub's.
func (m *model) Embed(_ context.Context, request providers.ModelEmbedRequest) (providers.ModelEmbedding, error) {
	vectors := make([][]float64, len(request.Texts))
	tokens := 0
	for i, text := range request.Texts {
		tokens += words(text)
		ws := strings.Fields(strings.ToLower(text))
		sort.Strings(ws)
		for _, seed := range []uint32{0x811c9dc5, 0x01000193, 0x9e3779b9} {
			hash := seed
			for _, w := range ws {
				for _, c := range w {
					// JavaScript's charCodeAt reads UTF-16 code units.
					for _, unit := range utf16Units(c) {
						hash = (hash ^ uint32(unit)) * 0x01000193
					}
				}
			}
			vectors[i] = append(vectors[i], float64(hash)/math.MaxUint32*2-1)
		}
	}
	return providers.ModelEmbedding{Vectors: vectors, Usage: &providers.ModelUsage{InputTokens: tokens}}, nil
}

func utf16Units(r rune) []uint16 {
	if r < 0x10000 {
		return []uint16{uint16(r)}
	}
	r -= 0x10000
	return []uint16{uint16(0xd800 + (r >> 10)), uint16(0xdc00 + (r & 0x3ff))}
}
