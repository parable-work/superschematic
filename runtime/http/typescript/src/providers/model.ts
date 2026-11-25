/*
The model provider (D159): completes one turn of an agent run (D158) with
the tools the run's role allows, and reports what the turn used. Content
from outside, a tool result that carries an inbound message, a web page or
a document, is marked untrusted, and a provider passes the mark on in the
way its API takes it. No credential value is ever in a message: a
credential reaches only the runner (credentials.ts).
*/

/** One message of a turn. */
export interface ModelMessage {
  readonly role: 'system' | 'user' | 'assistant' | 'tool';
  readonly content: string;
  /** For a tool message: the call it answers. */
  readonly toolCallId?: string;
  /** Content from outside the app, which is data and never instructions. */
  readonly untrusted?: boolean;
}

/** A tool the model may call. */
export interface ModelTool {
  readonly name: string;
  readonly description: string;
  /** The JSON Schema of its input. */
  readonly inputSchema: Readonly<Record<string, unknown>>;
}

/** A turn to complete. */
export interface ModelTurn {
  /** The model, by the provider's name for it; the provider's default when left out. */
  readonly model?: string;
  readonly messages: readonly ModelMessage[];
  readonly tools?: readonly ModelTool[];
  readonly maxTokens?: number;
}

/** A tool call the model made. */
export interface ModelToolCall {
  readonly id: string;
  readonly name: string;
  readonly input: unknown;
}

/** What a turn used, for the Budget behaviour (D104). */
export interface ModelUsage {
  readonly inputTokens: number;
  readonly outputTokens: number;
  /** The amount the provider charges, when it says, as a decimal string. */
  readonly amount?: { readonly value: string; readonly currency: string };
}

/** A completed turn. */
export interface ModelCompletion {
  readonly content: string;
  readonly toolCalls: readonly ModelToolCall[];
  readonly stop: 'end' | 'toolUse' | 'maxTokens';
  readonly usage: ModelUsage;
}

/** A model provider. */
export interface ModelProvider {
  complete(turn: ModelTurn): Promise<ModelCompletion>;
}
