// A model provider stub (D159) whose answers follow from the turn, so a test
// of an agent run knows what each turn does. The last user or tool message
// decides: "call <tool> <json>" calls that tool when the turn offers it,
// and anything else is answered "ok: <text>". Usage counts words, one
// token each; content marked untrusted is counted and never obeyed.
import { defineProvider, type ModelProvider } from '@superschematic/http-runtime/providers';

const words = (text: string) => text.split(/\s+/u).filter(Boolean).length;

export const stubModel = defineProvider({
  name: 'stub-model',
  implements: 'model',
  config: {
    MODEL: { optional: true, description: 'The model name the stub reports; stub-1 when unset.' },
  },
  create(): ModelProvider {
    let calls = 0;
    return {
      async complete(turn) {
        const inputTokens = turn.messages.reduce((sum, message) => sum + words(message.content), 0);
        const last = [...turn.messages].reverse().find(message => message.role === 'user' || message.role === 'tool');
        const text = last?.content ?? '';
        const match = /^call (\S+)(?: (.*))?$/su.exec(text);
        if (match && !last?.untrusted && turn.tools?.some(tool => tool.name === match[1])) {
          const input = match[2] ? (JSON.parse(match[2]) as unknown) : {};
          return { content: '', toolCalls: [{ id: `call-${++calls}`, name: match[1]!, input }], stop: 'toolUse', usage: { inputTokens, outputTokens: 1 } };
        }
        const content = `ok: ${text}`;
        const outputTokens = Math.min(words(content), turn.maxTokens ?? Number.POSITIVE_INFINITY);
        return { content, toolCalls: [], stop: outputTokens < words(content) ? 'maxTokens' : 'end', usage: { inputTokens, outputTokens } };
      },
      // Search's embeddings (D155): each text's vector is three FNV-1a
      // hashes of its words, so the same text always embeds alike.
      async embed(request) {
        const vectors = request.texts.map(text =>
          [0x811c9dc5, 0x01000193, 0x9e3779b9].map(seed => {
            let hash = seed;
            for (const word of text.toLowerCase().split(/\s+/u).filter(Boolean).sort()) {
              for (let i = 0; i < word.length; i++) hash = Math.imul(hash ^ word.charCodeAt(i), 0x01000193) >>> 0;
            }
            return (hash / 0xffffffff) * 2 - 1;
          })
        );
        return { vectors, usage: { inputTokens: request.texts.reduce((sum, text) => sum + words(text), 0), outputTokens: 0 } };
      },
    };
  },
});

export default stubModel;
