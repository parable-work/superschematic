/*
The model provider (D159): completes one turn of an agent run (D158) with
the tools the run's role allows, and reports what the turn used. Content
from outside, a tool result that carries an inbound message, a web page or
a document, is marked untrusted, and a provider passes the mark on in the
way its API takes it. No credential value is ever in a message: a
credential reaches only the runner (credentials.ts).
*/

/**
 * One message of a turn. An assistant message is an earlier completion of
 * the run, replayed: its content, the tool calls it made, which the tool
 * messages after it answer, and the provider's own state of it, as the
 * provider returned them. A provider builds its API's conversation from
 * these alone: a provider of a model API with tool-use blocks puts each
 * tool call before its result and passes the turn's reasoning blocks back
 * unchanged.
 */
export interface ModelMessage {
  readonly role: 'system' | 'user' | 'assistant' | 'tool';
  readonly content: string;
  /** For a tool message: the call it answers. */
  readonly toolCallId?: string;
  /** For an assistant message: the tool calls its completion made, in order. */
  readonly toolCalls?: readonly ModelToolCall[];
  /** For an assistant message: the providerState its completion returned, unchanged. */
  readonly providerState?: unknown;
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
  /**
   * The provider's own record of the turn, as JSON, which no one but the
   * provider reads: the content blocks a model API returned, its signed
   * reasoning blocks among them. The dispatcher keeps it with the
   * completion, unredacted while the run lives, and passes it back,
   * unchanged, on that assistant message in every later turn; a reader of
   * the transcript, and the transcript a run leaves when it ends, hold it
   * redacted. A provider that keeps none leaves it out.
   */
  readonly providerState?: unknown;
}

/** Texts to embed (D155): each becomes one vector, under model or the provider's default. */
export interface ModelEmbedRequest {
  readonly model?: string;
  readonly texts: readonly string[];
}

/** The vectors of an embed request, one per text in order, and what it used. */
export interface ModelEmbedding {
  readonly vectors: readonly (readonly number[])[];
  readonly usage?: ModelUsage;
}

/** A model provider. */
export interface ModelProvider {
  complete(turn: ModelTurn): Promise<ModelCompletion>;
  /** Embeds texts, for search's re-ranking (D155); a provider that embeds nothing leaves it out, and search runs on full text alone. */
  embed?(request: ModelEmbedRequest): Promise<ModelEmbedding>;
}
