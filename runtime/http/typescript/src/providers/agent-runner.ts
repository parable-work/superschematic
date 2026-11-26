import { useCredential, type CredentialProvider, type CredentialUse, type InjectionRunner, type InjectionTarget } from './credentials.js';
import type { ModelMessage, ModelToolCall } from './model.js';

/*
The program a sandbox instance runs for an agent run (D158), the other side
of the dispatcher's agent routes. It reads what the dispatcher gave the
instance: AGENT_ENDPOINT, AGENT_RUN_ID, AGENT_CREDENTIALS (references alone)
and the secret AGENT_RUN_TOKEN. Each turn it posts the tool results it has
to <endpoint>/turn, where the app's model provider completes the turn with
the run's tools, meters it against the budget and writes the transcript, so
the instance holds no model key and no budget of its own. Each tool call it
runs through the run's MCP endpoint (<endpoint>/mcp) as the run's token, or,
for a tool the sandbox runs itself, in process. A call an approval holds
(428 approval_required) is tried again until it runs, is denied, or
approvalWaitMs passes. A credential a tool needs is resolved here, with
useCredential: its value goes to the runner that uses it (a request's
header, a browser's field) and never to the endpoint, the model or a log;
the endpoint is told the reference, the provider, the target and the time.
When the model ends its turns, the runner reports the outcome at
<endpoint>/finish and exits; the dispatcher's watch ends the run.

Nothing here imports pg or Hono: it runs in any sandbox with fetch.
*/

/** What a tool the sandbox runs is given. */
export interface SandboxToolContext {
  /** Resolves a reference the run holds into target and hands the value to runner, recording the use with the run. */
  useCredential(ref: string, target: InjectionTarget, runner: InjectionRunner): Promise<CredentialUse>;
  /** The run's credential references, as AGENT_CREDENTIALS gave them. */
  readonly credentials: readonly { readonly ref: string; readonly site?: string; readonly header?: string; readonly selector?: string }[];
}

/** A tool the sandbox runs in process. */
export type SandboxToolHandler = (input: Readonly<Record<string, unknown>>, ctx: SandboxToolContext) => Promise<unknown>;

/** What runAgentInstance takes. */
export interface AgentInstanceOptions {
  /** The instance's environment: process.env. */
  readonly env: Readonly<Record<string, string | undefined>>;
  readonly fetch?: typeof fetch;
  /** The credential provider the instance resolves references through, and its name for the record. */
  readonly credentials?: { readonly provider: CredentialProvider; readonly name: string };
  /** The tools the sandbox runs itself, by name. */
  readonly tools?: Readonly<Record<string, SandboxToolHandler>>;
  /** How long a call waits for its approval, in milliseconds; 60000 unless set. */
  readonly approvalWaitMs?: number;
  /** How often a waiting call is tried again, in milliseconds; 1000 unless set. */
  readonly pollMs?: number;
  /** The most turns it takes; 20 unless set. */
  readonly maxTurns?: number;
}

/** What a run of the instance came to. */
export interface AgentInstanceResult {
  readonly outcome: 'done' | 'failed' | 'stopped';
  readonly turns: number;
  readonly summary: string;
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

/** The JSON-RPC answer of an MCP endpoint: its body, or the data of the one message event it streams. */
function rpcBody(text: string): unknown {
  const data = text
    .split(/\r?\n/u)
    .filter(line => line.startsWith('data:'))
    .map(line => line.slice(5).trim())
    .join('');
  return JSON.parse(data || text);
}

/** Runs an agent run's turns inside its sandbox instance until the model ends them or the run is stopped. */
export async function runAgentInstance(options: AgentInstanceOptions): Promise<AgentInstanceResult> {
  const env = options.env;
  const endpoint = (env.AGENT_ENDPOINT ?? '').replace(/\/+$/u, '');
  const token = env.AGENT_RUN_TOKEN ?? '';
  if (!endpoint || !token) throw new Error('runAgentInstance: AGENT_ENDPOINT and AGENT_RUN_TOKEN are the instance\'s');
  const call = options.fetch ?? globalThis.fetch;
  const credentials = JSON.parse(env.AGENT_CREDENTIALS ?? '[]') as SandboxToolContext['credentials'];
  const post = async (path: string, body: unknown): Promise<Response> =>
    call(`${endpoint}/${path}`, { method: 'POST', headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json', accept: 'application/json, text/event-stream' }, body: JSON.stringify(body) });

  const ctx: SandboxToolContext = {
    credentials,
    async useCredential(ref, target, runner) {
      if (!options.credentials) throw new Error('the instance has no credential provider');
      if (!credentials.some(c => c.ref === ref)) throw new Error(`the run holds no credential ${ref}`);
      return useCredential({
        provider: options.credentials.provider,
        providerName: options.credentials.name,
        ref,
        target,
        runner,
        record: async use => {
          await post('credential', { ref: use.ref, provider: use.provider, target: use.target, usedAt: use.usedAt.toISOString() });
        },
      });
    },
  };

  let rpc = 0;
  const mcp = async (name: string, args: unknown): Promise<{ text: string; problem?: { status?: number; code?: string } }> => {
    const response = await post('mcp', { jsonrpc: '2.0', id: ++rpc, method: 'tools/call', params: { name, arguments: args ?? {} } });
    if (response.status === 401) return { text: 'the run has ended', problem: { status: 401, code: 'unauthorized' } };
    const body = rpcBody(await response.text()) as { result?: { content?: { text?: string }[]; isError?: boolean; structuredContent?: { status?: number; code?: string } }; error?: { message?: string } };
    if (body.error) return { text: body.error.message ?? 'tool error', problem: { status: 400 } };
    const text = body.result?.content?.map(c => c.text ?? '').join('') ?? '';
    if (!body.result?.isError) return { text };
    let problem: { status?: number; code?: string } = body.result?.structuredContent ?? {};
    try {
      problem = { ...(JSON.parse(text) as { status?: number; code?: string }), ...problem };
    } catch {
      // A tool error whose text is no problem document.
    }
    return { text, problem };
  };

  const runTool = async (toolCall: ModelToolCall): Promise<string> => {
    const local = options.tools?.[toolCall.name];
    if (local) {
      try {
        return JSON.stringify(await local((toolCall.input ?? {}) as Record<string, unknown>, ctx));
      } catch (err) {
        return JSON.stringify({ error: err instanceof Error ? err.message : String(err) });
      }
    }
    const until = Date.now() + (options.approvalWaitMs ?? 60_000);
    for (;;) {
      const answer = await mcp(toolCall.name, toolCall.input);
      if (answer.problem?.status !== 428 || Date.now() > until) return answer.text;
      await sleep(options.pollMs ?? 1000);
    }
  };

  const messages: ModelMessage[] = [];
  let summary = '';
  for (let turn = 1; turn <= (options.maxTurns ?? 20); turn++) {
    const response = await post('turn', { messages });
    messages.length = 0;
    if (response.status === 409 || response.status === 401) return { outcome: 'stopped', turns: turn - 1, summary };
    if (!response.ok) {
      await post('finish', { outcome: 'failed', summary: `the turn answered ${response.status}` });
      return { outcome: 'failed', turns: turn, summary };
    }
    const completion = (await response.json()) as { content: string; toolCalls: ModelToolCall[]; stop: string };
    summary = completion.content;
    if (completion.toolCalls.length === 0) {
      await post('finish', { outcome: 'done', summary });
      return { outcome: 'done', turns: turn, summary };
    }
    for (const toolCall of completion.toolCalls) {
      messages.push({ role: 'tool', toolCallId: toolCall.id, content: await runTool(toolCall) });
    }
  }
  await post('finish', { outcome: 'done', summary });
  return { outcome: 'done', turns: options.maxTurns ?? 20, summary };
}
