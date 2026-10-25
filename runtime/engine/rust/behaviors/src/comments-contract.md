# Comments v2, batch 1: the contract

This note is the contract the Rust behavior (`comments.rs`), the Go
generators and the corpora implement for batch 1 of D77 ("Comments,
rebuilt", step 1 of its build order). The declaration is
`declarations/Comments.behavior.json`, the canonical copy of
`internal/registry/behaviors/comments.behavior.json`; where this note and
the declaration disagree, the declaration's schemas win and this note is
the bug.

Batch 1 is posts, edits, deletes, purge, reactions, pins, threads and
resolve, every writing operation beside the instance (D77 primitive 1,
`"beside": true`, logged). What batch 1 leaves out is listed at the end.

## Terms

- The *conversation* is every message of one instance.
- A *message* is *top-level* (no `thread`) or a *reply* (`thread` is the id
  of a top-level message). Threads are one level deep: a reply's `thread`
  never names a reply.
- A *thread* is one top-level message and its replies. Every top-level
  message is a thread, with replies or without, so a lone review comment
  can be resolved.
- A message is *present* while its row exists: `posted` or `deleted`. A
  purged message is gone, and every reference to its id (`thread`,
  `quotes`, `message`, `around`) finds `no_message`.
- *The caller* is the call's principal: `subject` is
  `principal.subject`; `via` is `principal.service.deployable` when
  `principal.service` is set (stands-in or not), else absent.
- `now` is the engine clock's time, `cx.now()`, in epoch milliseconds.
- "*Moderates*": the config names `moderatePermission` and
  `cx.can(moderatePermission)` is true. "*Resolves*" and "*pins*" likewise
  for `resolvePermission` and `pinPermission`.

## Config

Every member is optional; `{}` and no config are the same.

| Member | Type | When absent | Meaning |
|--------|------|-------------|---------|
| `maxBodyLength` | integer 1..40000 | 40000 | the longest `body` a `post` or an `edit` gives, in Unicode code points |
| `editWindowMs` | integer >= 1000 | no window | an author's `edit` is refused once `now - createdAt > editWindowMs`; a caller who moderates is not held to it. `deleteMessage` has no window |
| `blocks` | unique array of `"ref"`, `"action"` | every kind | the kinds a `post` or an `edit` may give; `[]` allows none |
| `moderatePermission` | string | nobody moderates | edit and delete another's message, edit past the window, purge |
| `pinPermission` | string | anyone the policy lets call `pin` | pin and unpin |
| `resolvePermission` | string | only the thread's author | resolve and reopen another's thread |

A config change applies to the next call: narrowing `blocks` or
`maxBodyLength` leaves present messages as they are, and an `edit` that
gives no `blocks` keeps blocks the config no longer allows.
`configChange`: Comments is added to a schema with instances, which start
with an empty conversation, and is not removed from one (their messages
would stay behind), as today.

## The message record

The record every operation returns for a message (and an event carries),
members in this order, absent members left out:

| Member | Type | Present | Value |
|--------|------|---------|-------|
| `id` | integer | always | the message's number on the instance, 1, 2, 3, ...; never reused, a purged one's included |
| `thread` | integer | replies | the id of the thread's top-level message |
| `quotes` | integer | when given | the id of the message of the thread the reply quotes; may name a message since purged |
| `author` | string | always | the poster's subject |
| `via` | string | when posted through a service | the calling service's deployable (D37) |
| `state` | `"posted"` or `"deleted"` | always | batch 2 adds `composing` and `interrupted` |
| `body` | string | always | Markdown; `""` once deleted |
| `blocks` | array | always | as posted or last edited, in order; `[]` once deleted |
| `mentions` | array of strings | always | as posted or last edited, in order; `[]` once deleted |
| `reactions` | object | always | by emoji, `{ count, mine }`; `{}` once deleted (see below) |
| `createdAt` | integer | always | when it was posted |
| `editedAt` | integer | after an edit | when it was last edited; absent once deleted |
| `edits` | integer | always | how many edits it has had; 0 once deleted |
| `pinned` | boolean | always | false once deleted |
| `replies` | integer | top-level | how many replies of the thread are `posted` |
| `lastReplyAt` | integer | top-level, with a posted reply | the greatest `createdAt` of the thread's posted replies |
| `participants` | array of strings | top-level | see below |
| `resolved` | `{ by, at }` | top-level, resolved | who resolved the thread and when |

The four top-level members are on every top-level record, a deleted one
included, and on no reply.

`reactions`: one member per emoji the message has reactions with, in the
order of each emoji's first reaction still held (the smallest `reaction`
number among its rows, "Storage"); `count` is how many subjects reacted
with it, `mine` whether the subject of *the principal the record is
computed for* is one of them. In a result that is the caller; in an
event's `result` it is that event's actor, so a client that merges events
takes `count` from them and keeps `mine` from its own reads and its own
events.

`participants`: the distinct `author`s of the thread's `posted` messages
(the top-level one and its replies), ordered by the id of each author's
latest posted message in the thread, greatest first, at most 10. A deleted
top-level message's author is in it only by a posted reply.

```sql
SELECT author FROM <messages>
 WHERE namespace = ? AND schema = ? AND id = ? AND state = 'posted' AND (message = :t OR thread = :t)
 GROUP BY author ORDER BY MAX(message) DESC LIMIT 10
```

### Blocks

A block is one of these, checked by the `paramsSchema` (a `oneOf` on
`kind`), and then by the behavior:

| Kind | Members | The behavior's checks |
|------|---------|-----------------------|
| `ref` | `{ kind: "ref", schema, id }`, `schema` `^[A-Za-z][A-Za-z0-9_-]{0,127}$`, `id` `^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$` | the instance exists in the call's namespace and the caller may read it: `cx.schemas().readable(schema)` is true and `cx.instances().get(schema, id, <no behavior fields>)` finds it. Anything else, an unknown schema included, is `ref_not_found`, one refusal for every case so a refusal tells the caller nothing it may not read |
| `action` | `{ kind: "action", title, status, data? }`, `title` 1..200 code points with a non-space, `status` `running`, `succeeded` or `failed`, `data` a JSON object | `data`, written as compact JSON (`serde_json::to_vec`), is at most 8192 bytes |

At most 20 blocks per message. A result's `blocks` are stored and returned
as given; the `resultSchema` checks only each block's `kind`.

On `edit`, a ref block is checked only when the message's blocks before
the edit hold no ref block with the same `schema` and `id`, so an edit
that moves an action's status does not fail on a ref whose instance has
since gone.

### Mentions

`mentions` is a list of subject strings (a person's, an agent's, a
group's as `role:<name>`), each 1..256 code points with no whitespace, at
most 50, unique; stored and returned in the order given. Batch 1 records
them and nothing more: no delivery, no check that a subject exists.

### Emoji

An emoji is a name or the character itself: 1..32 code points, no
whitespace (`^\S+$`). The behavior compares emoji as strings, byte for
byte: `:+1:` and `👍` are two emoji. A message holds at most 20 distinct
emoji.

## Refusals

Three kinds, as the other behaviors refuse:

- `invalid_argument` (`OperationParamsError`, 400): what the
  `paramsSchema` cannot say, as an issue at a JSON pointer, with the text
  given here.
- `forbidden` (403): the config names a permission and the caller lacks
  it; message `"<subject> may not <operation> on <schema> <id>: it needs
  permission <permission>"`, as Approvals and Revisions word theirs.
- A veto (`vetoed`, 409), with a code the declaration lists and the
  `details` given here.

| Veto code | details |
|-----------|---------|
| `no_message` | `{ message }`: the id that names nothing present |
| `not_a_thread` | `{ message, thread }`: the reply named, and the top-level message it is in |
| `not_in_thread` | `{ message, thread }`: the message `quotes` names, and the post's `thread` |
| `deleted` | `{ message }` |
| `not_author` | `{ message, author }`: for a thread, the top-level message and its author |
| `edit_window_passed` | `{ message, createdAt, editWindowMs }` |
| `too_many_edits` | `{ message, max: 500 }` |
| `already_reacted` | `{ message, emoji }` |
| `not_reacted` | `{ message, emoji }` |
| `too_many_reactions` | `{ message, max: 20 }` |
| `already_pinned` | `{ message }` |
| `not_pinned` | `{ message }` |
| `already_resolved` | `{ thread, by, at }` |
| `not_resolved` | `{ thread }` |
| `block_not_allowed` | `{ block, kind, allowed }`: the block's index in `blocks`, its kind, the config's kinds |
| `ref_not_found` | `{ block, schema, id }` |
| `not_configured` | `{ permission: "moderatePermission" }` |

Each operation checks in the order its table lists and stops at the first
refusal; for blocks, the first offending block by index. The engine's own
checks (policy, `paramsSchema`, `If-Match` as for a read) come before all
of them.

## Operations

Every writing operation is `"writes": true, "beside": true` and logged:
it appends one `operation` event with `beside: true`, `seq: null` and
`change: { behavior: "Comments", operation, params, result }`, its
`result` exactly the operation's result as the caller receives it. It
writes only Comments' tables and its `count` column, never calls
`update()`, never invokes or calls an operation that is not beside, and
never creates or deletes an instance. The read-only ones append nothing.

### `post({ body, blocks?, mentions?, thread?, quotes?, clientId? })`

Posts a message as the caller. Result and event `result`: the message
record.

| # | Refusal | When |
|---|---------|------|
| 1 | (retry) | `clientId` is given and a present message of the instance has `author` = the caller's subject and that `client_id`: the call returns that message's record as it is now, whatever the other params, writes nothing and calls `cx.unchanged()`, so no event is appended. A purged message's `clientId` is free again |
| 2 | `invalid_argument` `/body` `must NOT have more than <maxBodyLength> characters` | `body` is longer than `maxBodyLength` code points |
| 3 | `invalid_argument` `/quotes` `needs thread` | `quotes` without `thread` |
| 4 | `invalid_argument` `/blocks/<i>/data` `is more than 8192 bytes of JSON` | an action block's `data` |
| 5 | `no_message` | `thread` names no present message |
| 6 | `not_a_thread` | `thread` names a reply |
| 7 | `no_message` | `quotes` names no present message |
| 8 | `not_in_thread` | `quotes` names a message that is neither `thread` nor a reply in it |
| 9 | `deleted` | `quotes` names a deleted message |
| 10 | `block_not_allowed` | a block whose kind the config's `blocks` does not list |
| 11 | `ref_not_found` | a ref block's instance (see Blocks) |

Posting into the thread of a deleted top-level message is allowed, and so
is posting into a resolved thread, which stays resolved.

Writes: `message` = the conversation's `last_message + 1`, which becomes
`last_message`; the row with `thread`, `quotes`, `author` = subject,
`via`, `state = 'posted'`, `body`, `blocks` and `mentions` as given (`[]`
when absent), `created_at = now`, `edits = 0`, `pinned = 0`, `client_id`.
Then the counts ("Fields").

### `edit({ message, body?, blocks?, mentions? })`

Replaces the members given; the others stay. Result and event `result`:
the record after the edit.

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` at `""` `names none of body, blocks and mentions` | none of the three is given |
| 2 | `invalid_argument` `/body` `must NOT have more than <maxBodyLength> characters` | as for post |
| 3 | `invalid_argument` `/blocks/<i>/data` `is more than 8192 bytes of JSON` | as for post |
| 4 | `no_message` | `message` names no present message |
| 5 | `deleted` | the message is deleted |
| 6 | `not_author` | the caller is not its author and does not moderate |
| 7 | `edit_window_passed` | the config sets `editWindowMs`, the caller does not moderate, and `now - createdAt > editWindowMs` |
| 8 | `too_many_edits` | the message has 500 edits |
| 9 | `block_not_allowed` | as for post |
| 10 | `ref_not_found` | a ref block new to the message (see Blocks) |

An edit whose values equal the message's is still an edit: it is recorded
and counted. Writes: a `comment_edits` row numbered `edits + 1` holding
the `body`, `blocks` and `mentions` from *before* the edit, `edited_by` =
subject, `edited_at = now`; then the message's given members,
`edited_at = now`, `edits + 1`. No count moves.

### `deleteMessage({ message })`

`delete` is an operation every schema has, so a behavior cannot take the
name (`builtinOperations`); this is D77's `delete`. Result and event
`result`: the record after, `state: "deleted"`.

| # | Refusal | When |
|---|---------|------|
| 1 | `no_message` | |
| 2 | `deleted` | already deleted |
| 3 | `not_author` | the caller is not its author and does not moderate |

Writes: removes the message's `comment_edits` and `comment_reactions`
rows; sets `state = 'deleted'`, `body = ''`, `blocks = '[]'`,
`mentions = '[]'`, `edited_at = NULL`, `edits = 0`, `pinned = 0`. It keeps
`id`, `thread`, `quotes`, `author`, `via`, `created_at`, `client_id` (so
a retry of its post returns the deleted message) and a top-level
message's `resolved_*`. Its replies stay as they are. Then the counts.

### `purge({ message })`

Removes the message, and for a top-level message every reply in its
thread, with their edits and reactions. Result and event `result`:
`{ message, purged }`, `purged` every id removed in ascending order (the
message itself included). The event carries ids only, never a body.

| # | Refusal | When |
|---|---------|------|
| 1 | `not_configured` | the config names no `moderatePermission` |
| 2 | `forbidden` | the caller lacks `moderatePermission` |
| 3 | `no_message` | |

A deleted message purges like a posted one. Another reply's `quotes` that
names a purged reply keeps the id. The ids are not reused
(`last_message` does not move). Then the counts.

### `react({ message, emoji })` and `unreact({ message, emoji })`

Result and event `result`: the record after.

| # | `react` refusal | When |
|---|-----------------|------|
| 1 | `no_message` | |
| 2 | `deleted` | |
| 3 | `already_reacted` | the caller holds a reaction with `emoji` on the message |
| 4 | `too_many_reactions` | the message has reactions with 20 distinct emoji and none with `emoji` |

| # | `unreact` refusal | When |
|---|-------------------|------|
| 1 | `no_message` | |
| 2 | `deleted` | |
| 3 | `not_reacted` | the caller holds no reaction with `emoji` on the message |

`react` inserts a row numbered `last_reaction + 1` (which becomes
`last_reaction`), `reacted_at = now`; `unreact` deletes the caller's row.

### `pin({ message })` and `unpin({ message })`

Top-level messages and replies alike. Result and event `result`: the
record after.

| # | `pin` refusal | When |
|---|---------------|------|
| 1 | `forbidden` | the config names `pinPermission` and the caller lacks it |
| 2 | `no_message` | |
| 3 | `deleted` | |
| 4 | `already_pinned` | |

| # | `unpin` refusal | When |
|---|-----------------|------|
| 1 | `forbidden` | as for pin |
| 2 | `no_message` | |
| 3 | `not_pinned` | a deleted message is not pinned, so this covers it |

### `resolve({ thread })` and `reopen({ thread })`

Result and event `result`: the top-level record after.

| # | Refusal | When |
|---|---------|------|
| 1 | `no_message` | `thread` names no present message |
| 2 | `not_a_thread` | it names a reply |
| 3 | `deleted` | the top-level message is deleted |
| 4 | `not_author` | the caller is not the top-level message's author and does not resolve (`resolvePermission`) |
| 5 | `already_resolved` (resolve) / `not_resolved` (reopen) | |

`resolve` sets `resolved_by` = subject, `resolved_at = now`; `reopen`
sets both NULL. Then the counts.

### `listMessages({ thread?, around?, before?, after?, limit? })`

Read-only. The *listing* is the instance's top-level messages when
`thread` is absent, and the replies whose `thread` is `thread` when it is
given; deleted messages are in it, purged ones are not. A page is at most
`limit` (1..100, 50 when absent) messages of the listing, always in
ascending id order.

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` at the later one's pointer (`/before` or `/after`), `must not be given with <the earlier one>` | more than one of `around`, `before`, `after`, taken in that order |
| 2 | `no_message` | `thread` names no present message |
| 3 | `not_a_thread` | `thread` names a reply |
| 4 | `no_message` | `around` names no message of the listing (absent, purged, or a message outside it: a reply when listing top-level messages, or a message of another thread) |

`before` and `after` are bounds, not references: an id that names no
message is never refused.

| Anchor | The page |
|--------|----------|
| none | the listing's greatest `limit` ids: the latest page |
| `before: B` | the greatest `limit` ids below `B` |
| `after: A` | the least `limit` ids above `A` |
| `around: M` | the greatest `(limit - 1) / 2` ids below `M` (integer division), `M`, then the least `limit - 1 - (limit - 1) / 2` ids above `M`; a side with fewer is not filled from the other |

Result `{ items, before, after, root? }`:
- `before`: the first item's id when the listing has a message with a
  lower id, else null; pass it as `before` for the older page.
- `after`: the last item's id when the listing has a message with a
  higher id, else null; pass it as `after` for the newer page.
- An empty page has both null.
- `root`: with `thread`, the thread's top-level record; absent without.

A client opens a conversation with no anchor, scrolls up with `before`,
catches up with `after`, and jumps to a message with `around`.

### `getMessage({ message })`

Read-only. The record, a deleted message's included. Refusal: `no_message`.

### `listEdits({ message, limit?, cursor? })`

Read-only. What the message held before each edit, by edit number
ascending, paged with the engine's `page_request` and `page` over the
edit number (`limit` 1..500, 50 when absent; `cursor` the previous page's
`next`; a cursor it did not write is `invalid_argument` at `/cursor`).
Items `{ edit, body, blocks, mentions, by, at }`: the message's values
before edit number `edit`, and who made that edit and when. A deleted
message has none. Refusal: `no_message`.

### `listReactions({ message, emoji, limit?, cursor? })`

Read-only. The subjects that reacted to the message with `emoji`, by
`reaction` number ascending (the order they reacted), paged as
`listEdits` over the reaction number. Items `{ subject, at }`. An emoji
with no reactions is an empty page. Refusal: `no_message`.

## Fields

All three are `"volatile": true`: no event carries their moves (D77
primitive 1). Each is computed at the read from Comments' storage, and
each writing operation keeps the stored counts equal to these
definitions in the same call (incrementally or by recounting; both must
give these values):

| Field | Value | Read from | Moved by |
|-------|-------|-----------|----------|
| `messages` | integer: the conversation's `posted` messages, top-level and replies | the instance column `count` | post, deleteMessage, purge |
| `lastMessageAt` | the greatest `createdAt` of a `posted` message; absent while there is none | `comment_conversations.last_message_at` | post, deleteMessage, purge |
| `threads` | `{ open, resolved }`: `posted` top-level messages with `resolved_at` NULL, and with it set | `comment_conversations.open_threads`, `.resolved_threads` | post (top-level), deleteMessage and purge (top-level), resolve, reopen |

An instance with no `comment_conversations` row reads `lastMessageAt`
absent and `threads` `{ open: 0, resolved: 0 }`; `messages` reads `count`,
0 for a new instance.

## Storage

Logical names, as `cx.sql().table(name)` prefixes them. Migration 1
(`comments`) stays as shipped: the instance column `count` and the table
`comments`.

### Migration 2, `conversation`

It creates four tables, copies the comments into the first and drops
`comments`. A new table rather than columns on `comments`, because the
message's columns get their own names (`message`, `author`), because
`for` (batch 4) is a reserved word in both dialects and is spelled
`for_subject` in a later migration anyway, and because a migration has no
relation over the instances, so the counts a read needs beyond `count`
live in a table of their own (`comment_conversations`) that a migration
can fill.

`comment_messages`, primary key `(namespace, schema, id, message)`:

| Column | Spec | Holds |
|--------|------|-------|
| `namespace`, `schema`, `id` | text not null | the instance |
| `message` | integer not null | the id |
| `thread` | integer | NULL for a top-level message |
| `quotes` | integer | |
| `author` | text not null | |
| `via` | text | |
| `state` | text not null | `posted` or `deleted`; no CHECK, since batch 2 adds states |
| `body` | text not null | |
| `blocks` | text not null | compact JSON array |
| `mentions` | text not null | compact JSON array |
| `created_at` | integer not null | |
| `edited_at` | integer | |
| `edits` | integer not null default 0 | |
| `pinned` | integer not null default 0 | 0 or 1 |
| `client_id` | text | |
| `resolved_by` | text | |
| `resolved_at` | integer | |

Indexes: `comment_messages_thread` on `(namespace, schema, id, thread,
message)`, for both listings (`thread IS NULL` and `thread = ?`); and
`comment_messages_client`, unique, on `(namespace, schema, id, author,
client_id)` where `client_id IS NOT NULL`.

`comment_edits`, primary key `(namespace, schema, id, message, edit)`:
`namespace`, `schema`, `id` text not null; `message`, `edit` integer not
null; `body`, `blocks`, `mentions` text not null (the values before the
edit); `edited_by` text not null; `edited_at` integer not null.

`comment_reactions`, primary key `(namespace, schema, id, message, emoji,
subject)`: `namespace`, `schema`, `id` text not null; `message` integer
not null; `emoji`, `subject` text not null; `reaction` integer not null
(the conversation's `last_reaction + 1` at insert: the order of
reactions, and `listReactions`' paging number); `reacted_at` integer not
null. Index `comment_reactions_order` on `(namespace, schema, id,
message, emoji, reaction)`.

`comment_conversations`, primary key `(namespace, schema, id)`: one row
per instance that has had a message. `last_message` integer not null (the
greatest id ever assigned), `last_reaction` integer not null default 0,
`last_message_at` integer, `open_threads` and `resolved_threads` integer
not null default 0. `post` inserts the row when it is absent.

The instance column `count` keeps its meaning, the posted messages, which
every existing comment is.

### Moving v1's comments

In `up`, for each instance's comments in ascending `comment` order:

- `thread`: NULL when `reply_to` is NULL; otherwise the root of the
  `reply_to` chain, the first comment up the chain whose `reply_to` is
  NULL. Since a reply always names an earlier comment, one ascending pass
  computes it: `thread(c) = thread(reply_to(c))`, or `reply_to(c)` when
  that is NULL. A `reply_to` that names no comment (none can) makes the
  comment top-level.
- `quotes`: `reply_to` when it is not the thread's root (it names a
  reply), else NULL.
- `message` = `comment`, `author` = `created_by`, `created_at` as it was,
  `state = 'posted'`, `body` as it was, `blocks = '[]'`,
  `mentions = '[]'`, `via`, `edited_at`, `client_id`, `resolved_*` NULL,
  `edits = 0`, `pinned = 0`.

Then one `comment_conversations` row per instance with comments:
`last_message = MAX(comment)`, `last_message_at = MAX(created_at)`,
`open_threads` = its top-level comments, `resolved_threads = 0`,
`last_reaction = 0`. Then `DROP TABLE comments`. A recursive CTE does
the same in SQL on both dialects, if the implementation prefers.

### Deleting the instance

`afterChange` on `Delete` removes the instance's rows from all four
tables.

## What a client derives from the stream

A beside event carries the record, so a client that watches one
instance's events can show the conversation without reading. What the
record of one message does not carry, a client derives: on a reply's
`post`, the top-level message's `replies` grows by one, `lastReplyAt`
becomes the reply's `createdAt` and its author moves to the front of
`participants` (at most 10); on a reply's `deleteMessage` or `purge`, a
client that needs the top-level message's members exactly reads it with
`getMessage`.

## Left for later batches

Not declared and not built in batch 1, each in D77's order:

- Batch 2 (signals): `begin`, `appendDraft`, `finish`; the `composing`
  and `interrupted` states; `composeTimeoutMs`; the `typing`, `viewing`
  and `draft` signals; the `composing` field; `comment_drafts`.
- Batch 3 (namespace operations): `markRead`, `follow`, the `unread`
  field, `inbox`, `claimDeliveries`, `settleDeliveries`,
  `deliverPermission`, `maxAttempts`; `comment_reads`, `comment_follows`,
  `comment_deliveries`; deliveries for mentions and followers.
- Batch 4 (acting for a subject): the record's `for`, stored as
  `for_subject`.
- Batch 5: `searchMessages`; `code` and `attachment` blocks and the config
  kinds for them; `moveAnchor` and `anchorPermission`.

Each later batch adds its own migration (3, 4, ...) rather than the single
migration 2 D77 describes, since batch 1 ships alone.
