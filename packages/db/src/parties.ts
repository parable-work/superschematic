/*
Outside parties and inbound messages (D157): the tables whose rows are
parties, the conversations' tables, and the queue of run requests.
*/

/** The options of @party. */
export interface PartyOptions<T> {
  /** The field a party is named by in triage and on its timeline. */
  readonly title?: keyof T & string;
}

/**
 * Marks a table whose rows are parties (D157): people or organisations
 * outside the user table, principals of kind party that act only through
 * the grants they hold (D156). A party is reached at, and recognised by,
 * the channel identities that resolve to it.
 *
 * ```ts
 * @party<Contact>({ title: "name" })
 * export abstract class Contact { @key id: AutoGenerate<Identity.UUID>; name: string; }
 * ```
 */
export function party<T>(_options?: PartyOptions<T>): ClassDecorator {
  return () => {};
}

/** The parts of the conversations, each a class the build fills. */
export type ConversationsPart = "conversations" | "messages";

/**
 * Marks a class as one of the conversations' tables (D157), whose fields
 * the build fills: the conversations, one per party and channel, and the
 * messages, each inbound or outbound message of one, what became of it,
 * and who sent it for whom. The messages class may take @references and
 * @attachments: a message appears on each row it references, and holds
 * its files.
 *
 * ```ts
 * @conversations("conversations") export abstract class Conversation {}
 * @conversations("messages")
 * @references<Message>({ cleared: ReferenceCleared })
 * @attachments<Message>({ released: AttachmentReleased })
 * export abstract class Message {}
 * ```
 */
export function conversations(_part: ConversationsPart): ClassDecorator {
  return () => {};
}

/**
 * Marks the @queue class that is the queue of run requests (D157), whose
 * fields the build fills: an inbound message from a party with an active
 * grant puts { trigger: "inbound", party, message, grant } on it, which an
 * agent run takes.
 *
 * ```ts
 * @queue() @runRequests() export abstract class RunRequest {}
 * ```
 */
export function runRequests(): ClassDecorator {
  return () => {};
}
