# Comments v2, batches 1 to 5: the contract

This note is the contract the Rust behavior (`comments.rs`), the Go
generators and the corpora implement for batches 1 to 5 of D77
("Comments, rebuilt", steps 1 to 5 of its build order). The declaration is
`declarations/Comments.behavior.json`, the canonical copy of
`internal/registry/behaviors/comments.behavior.json`; where this note and
the declaration disagree, the declaration's schemas win and this note is
the bug.

Batch 1 is posts, edits, deletes, purge, reactions, pins, threads and
resolve, every writing operation beside the instance (D77 primitive 1,
`"beside": true`, logged). Batch 2, streamed replies over signals (D77
primitive 2), is "Batch 2" below: where it amends a rule of batch 1 it
says so, and the amended rule holds. Batch 3, read state, follows, the
inbox and deliveries over namespace operations (D77 primitive 3), is
"Batch 3" after it, and amends both the same way. Batches 4 and 5,
acting-for, search, code blocks and attachments (D77 primitive 4 and step
5), are "Batch 4 and 5" after that, amending the three before. What they
leave out is listed at the end.

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
| `editWindowMs` | integer >= 1000 | no window | an author's `edit` is refused once `now - createdAt > editWindowMs` (batch 2: `postedAt`); a caller who moderates is not held to it. `deleteMessage` has no window |
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
| `state` | `"posted"` or `"deleted"` | always | batch 2 adds `composing` and `interrupted` ("Batch 2", "The record") |
| `body` | string | always | Markdown; `""` once deleted; a composing message's draft so far (batch 2) |
| `blocks` | array | always | as posted or last edited, in order; `[]` once deleted |
| `mentions` | array of strings | always | as posted or last edited, in order; `[]` once deleted |
| `reactions` | object | always | by emoji, `{ count, mine }`; `{}` once deleted (see below) |
| `createdAt` | integer | always | when it was posted |
| `editedAt` | integer | after an edit | when it was last edited; absent once deleted |
| `edits` | integer | always | how many edits it has had; 0 once deleted |
| `pinned` | boolean | always | false once deleted |
| `replies` | integer | top-level | how many replies of the thread are `posted` |
| `lastReplyAt` | integer | top-level, with a posted reply | the greatest `createdAt` of the thread's posted replies (batch 2: the greatest `postedAt` of its standing replies) |
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
them and nothing more: no delivery, no check that a subject exists. Batch 3
records a delivery for each ("Batch 3", "Deliveries"), and still checks no
subject.

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
| `edit_window_passed` | `{ message, createdAt, editWindowMs }` (batch 2: `postedAt` in place of `createdAt`) |
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
| 7 | `edit_window_passed` | the config sets `editWindowMs`, the caller does not moderate, and `now - createdAt > editWindowMs` (batch 2: `postedAt`) |
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
| 1 | `invalid_argument` at the later one's pointer (`/before` or `/after`), `must not be given with <the earlier one>` | more than one of `around`, `before`, `after`, taken in that order; one issue, for the first two given (all three is `/before`, `must not be given with around`) |
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
`next`; a cursor it did not write is `invalid_argument` at `/cursor`,
checked before the message is looked up).
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
| `lastMessageAt` | the greatest `createdAt` of a `posted` message; absent while there is none (batch 2: the greatest `postedAt` of a standing message) | `comment_conversations.last_message_at` | post, deleteMessage, purge |
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

`afterChange` on `Delete` removes the instance's rows from
`comment_messages`, `comment_edits` and `comment_reactions`, and keeps
its `comment_conversations` row with `last_message` and `last_reaction`
as they were, `last_message_at = NULL` and both thread counts 0. An
instance re-created with the id numbers its messages and reactions on
from those high-water marks, so an id is never reused across the lives
one instance's events span, which a client that rebuilds from them sees
whole.

## What a client derives from the stream

A beside event carries the record, so a client that watches one
instance's events can show the conversation without reading. What the
record of one message does not carry, a client derives: on a reply's
`post`, the top-level message's `replies` grows by one, `lastReplyAt`
becomes the reply's `createdAt` (batch 2: its `postedAt`, which a `finish` or an `interrupt` event's record carries too) and its author moves to the front of
`participants` (at most 10); on a reply's `deleteMessage` or `purge`, a
client that needs the top-level message's members exactly reads it with
`getMessage`.

## Batch 2: streamed replies and signals

Batch 2 is D77's step 2 on Comments' side: a message streamed as it is
written (`begin`, `appendDraft`, `finish`, and `interrupt` for one whose
author stopped), the `composing` and `interrupted` states, the
`composing` field, `composeTimeoutMs`, and the signals `typing`,
`viewing` and `draft` (D77 primitive 2). The engine's side of signals,
their route, their stream message, their limits and `cx.signal`, is the
signals work's; this section says only what Comments declares and does
with them.

### Terms

- A message is *composing* from its `begin` until its `finish`, its
  `interrupt`, its `deleteMessage` or its purge. A composing message holds
  its id and its place in the listing, and its *draft*, the text its
  `appendDraft`s added, in order.
- A message *stands* while it is `posted` or `interrupted`: it holds text
  its author wrote, and the counts count it. Its `postedAt` is when it
  came to stand: a `post`'s `now`, or the `now` of the `finish` or the
  `interrupt` of a message `begin` started.
- Lengths are in Unicode code points, as `maxBodyLength` is: a draft's
  length, an `offset`, the result's `length`. A JavaScript client counts
  them with `Array.from(text).length`, never `text.length`.
- A message is *idle* when it is composing and
  `now - lastAppendAt >= composeTimeoutMs`, `lastAppendAt` being its
  `last_append_at` ("Migration 3").
- A text *has no text* when it is empty or every code point of it is
  white space: when it fails the `pattern` `\S` that post's `body` member
  declares, decided as the engine decides that pattern, so a draft
  `finish` accepts is a body `post` accepts.

### Config

| Member | Type | When absent | Meaning |
|--------|------|-------------|---------|
| `composeTimeoutMs` | integer 5000..3600000 | 60000 | how long a composing message's author may append nothing before the runner interrupts it ("The schedule") |
| `maxDrafts` | integer 1..50 | 5 | the most composing messages one subject may have on an instance at once; `begin` refuses another (`too_many_drafts`) |

`editWindowMs` (batch 1) counts from the message's `postedAt`, not its
`createdAt`: an author's `edit` is refused once
`now - postedAt > editWindowMs`. For a posted message the two are the
same; a streamed reply's window opens at its `finish` or `interrupt`.

`maxBodyLength` also bounds the draft: an `appendDraft` that would make it
longer is refused, and `finish` checks the body it posts against it. A
config narrowed while a message is composing leaves its draft as it is;
`finish` without `body` then refuses it, and `interrupt` keeps it whole.
Lowering `maxDrafts` leaves a caller's composing messages as they are;
its next `begin` is refused until it has fewer than the new maximum.

### The record

Amends batch 1's table:

| Member | Batch 2 |
|--------|---------|
| `state` | `"composing"`, `"posted"`, `"interrupted"` or `"deleted"` |
| `author` | the subject that posted or began it |
| `body` | while composing, the draft so far: its chunks concatenated in `chunk_offset` order; `""` right after `begin` |
| `blocks`, `mentions` | `[]` while composing and once interrupted, until an edit gives them |
| `reactions` | `{}` while composing (nothing can react to it) |
| `createdAt` | for a message `begin` started, the `begin`'s `now`; it never changes, so the message keeps its place |
| `postedAt` | new, integer, right after `createdAt`: when the message came to stand ("Terms"); for a post, equal to `createdAt`. Absent while composing, and on a message deleted while composing or interrupted with no text; a message deleted after it stood keeps it, as it keeps `createdAt`. An author's edit that moves an interrupted message to `posted` leaves it |
| `editedAt`, `edits` | absent and 0 while composing |
| `pinned` | false while composing |
| `replies` | how many replies of the thread *stand* |
| `lastReplyAt` | the greatest `postedAt` of the thread's standing replies |
| `participants` | the distinct `author`s of the thread's *standing* messages, ordered as batch 1 orders them |

The SQL of "participants" and every count of batch 1 read
`state IN ('posted', 'interrupted')` where batch 1 reads
`state = 'posted'`. A composing top-level record has `replies: 0`, no
`lastReplyAt`, `participants: []` (its own author is in them once it
stands) and no `resolved`. The record carries no draft length and no
`lastAppendAt`: a reader counts the body's code points.

The members are in batch 1's order, with `postedAt` between `createdAt`
and `editedAt`.

### Signals

Comments declares three, each `{ name, description, paramsSchema,
access }`:

| Signal | Data | Access | Sent by |
|--------|------|--------|---------|
| `typing` | `{ thread? }`: `thread` an integer >= 1 | `read` | a client, every few seconds while its person types; `thread` the top-level message whose thread the reply is for, absent for a top-level message |
| `viewing` | `{}` | `read` | a client, every few seconds while it shows the conversation |
| `draft` | `{ message, offset, text }`: `message` integer >= 1, `offset` integer >= 0, `text` a string of 1..4000 code points | `operation` | `appendDraft` alone, through `cx.signal("draft", data)`, when its call commits |

- Comments has no handler for `typing` and `viewing`: the engine checks
  their data against the `paramsSchema` and the policy (`read`, operation
  `signal:typing` or `signal:viewing`), and delivers them. `typing`'s
  `thread` is not checked against the conversation. A client shows a
  sender as typing or looking for a few seconds after its last signal;
  the engine holds no presence.
- `draft`'s data members are in the order `message`, `offset`, `text`.
  `offset` is the draft's length before `text`. The engine stamps `from`,
  the author's subject, and `at`.
- Every `draft` fits the engine's 4096 bytes of data: `appendDraft`
  refuses a `text` of more than 4000 bytes written as a JSON string, its
  quotes included (`serde_json::to_vec(&Value::String(text))` in Rust,
  `JSON.stringify(text)` in TypeScript, which write a string's bytes
  alike), and the rest of the data, `{"message":<at most 19
  digits>,"offset":<at most 5 digits>,"text":` and `}`, is at most 54
  bytes. So `cx.signal` is never refused for its size.

What a reader does with `draft` (the generators' docs and the client
tests follow this):

1. Open the stream with `signals=1` before reading, then read
   (`listMessages`, `getMessage`), and keep the signals that arrive
   during the read for after it.
2. For each composing message, hold its draft and its length `held`.
3. On a `draft` of a message the reader holds as composing: when
   `offset == held`, append `text`; when `offset < held`, ignore it (it
   is one the read already held); when `offset > held`, a signal was
   lost: read the message with `getMessage` and hold its `body`.
4. On a `draft` of a message the reader does not hold, or holds as not
   composing: read it with `getMessage` when it does not hold it, and
   ignore the signal otherwise. Delivery is at most once and in no order
   with the events, so a `draft` can arrive after the `finish` event of
   its message.
5. On a `finish`, `interrupt`, `deleteMessage` or `purge` event, take the
   record (or the purge) from the event and drop the held draft.

### The schedule

Comments declares one schedule, `interrupt`:

- `every`: from the config, `min(composeTimeoutMs / 4, 15000)`
  milliseconds (integer division), 15000 under the default; always at
  least 1250, above the engine's minimum.
- Each run reads the idle messages of the run's namespace and schema,
  oldest `last_append_at` first, a keyset of `(last_append_at, id,
  message)` past each page of 100, at most 1000 a run, through the index
  `comment_messages_idle` ("Migration 3"), as Timers' `fire` sweeps its
  deadlines:

  ```sql
  SELECT id, message, last_append_at FROM <comment_messages>
   WHERE namespace = ? AND schema = ? AND last_append_at IS NOT NULL
     AND last_append_at <= ?                -- now - composeTimeoutMs
     [AND (last_append_at, id, message) > (?, ?, ?)]
   ORDER BY last_append_at, id, message LIMIT ?
  ```

- For each row it invokes `interrupt({ message })` on the instance as
  the runner's principal (`cx.instances().invoke(schema, id, "interrupt",
  {"message": message}, None)`). A veto leaves the row (the author
  appended or finished in between) and the keyset moves past it; any
  other error fails the run, so a runner principal the policy does not
  let write the schema shows in `engine.runner.status()`, as Timers'
  does.
- Each interrupt is a logged beside event of its own, whose actor is the
  runner's principal. The schedule writes nothing itself.

The engine side must let a schedule invoke a beside operation; batch 1's
testkit covers beside operations called by a principal only.

### Operations

Each new writing operation is `"writes": true, "beside": true`, as batch
1's are; `begin`, `finish` and `interrupt` are logged, and `appendDraft`
is `"log": false` and appends no event.

#### `begin({ thread?, quotes?, clientId? })`

Begins a message as the caller, composing. Result and event `result`: the
record.

| # | Refusal | When |
|---|---------|------|
| 1 | (retry) | as post's 1: `clientId` is given and a present message of the instance, in any state, has `author` = the caller's subject and that `client_id`; it returns that message's record as it is now and calls `cx.unchanged()`. `post` and `begin` share a caller's `clientId`s (one unique index): a `post` retried as a `begin`, or the reverse, returns the message the first made |
| 2 | `invalid_argument` `/quotes` `needs thread` | `quotes` without `thread` |
| 3 | `no_message` | `thread` names no present message |
| 4 | `not_a_thread` | `thread` names a reply |
| 5 | `composing` `{ message: thread }` | `thread` names a composing message |
| 6 | `no_message` | `quotes` names no present message |
| 7 | `not_in_thread` | as post's |
| 8 | `deleted` | `quotes` names a deleted message |
| 9 | `composing` `{ message: quotes }` | `quotes` names a composing message |
| 10 | `too_many_drafts` `{ composing, max }` | the caller already has `maxDrafts` (5 when absent) or more composing messages on the instance; `composing` how many, `max` the config's `maxDrafts` |

Refusal 10 comes after what the call names, so a `begin` that names a
missing thread learns that first, and after the retry, so a retry of a
`begin` that made the caller's fifth draft still returns it. It counts
through `comment_messages_composing`:

```sql
SELECT COUNT(*) AS composing FROM <comment_messages>
 WHERE namespace = ? AND schema = ? AND id = ? AND author = ? AND last_append_at IS NOT NULL
```

Writes: `message` = the conversation's `last_message + 1`, which becomes
`last_message` (the conversation row inserted when absent, as `post`
does); the row with `thread`, `quotes`, `author` = subject, `via`,
`state = 'composing'`, `body = ''`, `blocks = '[]'`, `mentions = '[]'`,
`created_at = now`, `posted_at` NULL, `edited_at` NULL, `edits = 0`,
`pinned = 0`, `client_id`, `resolved_*` NULL, `draft_length = 0`,
`last_append_at = now`. No count moves: `messages`, `lastMessageAt` and
`threads` stay; `composing` gains the caller when it held no composing
message.

#### `appendDraft({ message, text })`

`text` is a string of 0..4000 code points (`paramsSchema`). Result
`{ message, offset, length }`: `offset` the draft's length before the
append, `length` after; both equal for an empty `text`. No event.

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` `/text` `is more than 4000 bytes as a JSON string` | `text`, written as a JSON string with its quotes ("Signals"), is more than 4000 bytes |
| 2 | `no_message` | |
| 3 | `not_composing` `{ message, state }` | the message is `posted`, `interrupted` or `deleted`; `state` is its state |
| 4 | `not_author` `{ message, author }` | the caller's subject is not the author; no permission lets another write a draft |
| 5 | `draft_too_long` `{ message, length, append, maxBodyLength }` | `length` (the draft's) + `append` (the text's length) > `maxBodyLength` |

An idle message not yet interrupted is still composing: an append to it is
allowed and makes it not idle.

Writes, when `text` is not empty: a `comment_drafts` row with
`chunk_offset` = `draft_length` and `text`; `draft_length` +=
the text's length. Always: `last_append_at = now`. Nothing else moves.
Then, when `text` is not empty, `cx.signal("draft", { message, offset,
text })`, delivered after the commit and dropped with a rollback. An
empty `text` writes no chunk and sends no signal: it keeps the message
from being idle, for an author at work with nothing to show (an agent
running a tool, say). A refused append writes nothing and moves no
`last_append_at`.

#### `finish({ message, body?, blocks?, mentions? })`

Posts a composing message. `body`, `blocks` and `mentions` have post's
`paramsSchema`. Result and event `result`: the record, `state: "posted"`.
The event carries the whole body, as a post's does.

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` `/body` `must NOT have more than <maxBodyLength> characters` | `body` is given and is longer than `maxBodyLength` |
| 2 | `invalid_argument` `/blocks/<i>/data` `is more than 8192 bytes of JSON` | as for post |
| 3 | `no_message` | |
| 4 | `not_composing` `{ message, state }` | as for appendDraft. A retry of a `finish` that committed meets this with `state: "posted"` |
| 5 | `not_author` `{ message, author }` | as for appendDraft |
| 6 | `invalid_argument` `/body` `is required: the draft has no text` | `body` is absent and the draft has no text |
| 7 | `invalid_argument` `/body` `must NOT have more than <maxBodyLength> characters` | `body` is absent and the draft is longer than `maxBodyLength` (the config narrowed since the appends) |
| 8 | `block_not_allowed` | as for post |
| 9 | `ref_not_found` | each ref block, as for post: the message held none |

`quotes` and `thread` were checked at `begin` and are not checked again:
a quoted message deleted or purged since stays quoted, as a posted
reply's does.

Writes: `state = 'posted'`; `body` = `body` when given, else the draft;
`blocks` and `mentions` as given (`'[]'` when absent); the message's
`comment_drafts` rows deleted; `draft_length` and `last_append_at` NULL.
`posted_at = now`; `created_at` stays. A `body` given in place of the
draft drops the draft; it is not an edit (`edits` stays 0). Then the
counts, as a post's: `messages + 1`, `open + 1` for a top-level message,
`last_message_at` the greater of itself and `now`.

#### `interrupt({ message })`

Stops a composing message. Result and event `result`: the record after,
`state: "interrupted"`, or `"deleted"` when the draft had no text.

| # | Refusal | When |
|---|---------|------|
| 1 | `no_message` | |
| 2 | `not_composing` `{ message, state }` | as for appendDraft |
| 3 | `not_idle` `{ message, author, lastAppendAt, composeTimeoutMs }` | the caller's subject is not the author, the message is not idle, and the caller does not moderate; `composeTimeoutMs` as the config gives it, 60000 when absent |

Its author may at any time (to stop and keep what it wrote; it drops the
draft with `deleteMessage`), a moderator may at any time, and anyone the
policy lets write may once the message is idle, which is how the
schedule calls it. Ask `cx.can(moderatePermission)` only when the first
two conditions of refusal 3 hold.

Writes, when the draft has text: `state = 'interrupted'`, `body` = the
draft, whole, even past a narrowed `maxBodyLength`; `posted_at = now`;
`blocks` and `mentions` stay `'[]'`; then the counts as `finish` moves
them. When the draft has no text: `state = 'deleted'`, `body = ''`, as
`deleteMessage` leaves a message, `posted_at` stays NULL, and no count
moves. Both: the message's
`comment_drafts` rows deleted, `draft_length` and `last_append_at` NULL;
`created_at` stays.

#### Batch 1's operations, amended

| Operation | Batch 2 |
|-----------|---------|
| `post` | refusals 1 to 13 below; writes `posted_at = now`, equal to `created_at` |
| `edit` | after refusal 5 (`deleted`), `composing` `{ message }` for a composing message. Refusal 7, `edit_window_passed`, counts from `postedAt`: `now - postedAt > editWindowMs`, with details `{ message, postedAt, editWindowMs }` in place of batch 1's `createdAt`. An interrupted message is edited as a posted one; an edit whose caller is its author also sets `state = 'posted'` (the author has said what it meant), and a moderator's edit of another's leaves it `interrupted`. No count moves: both stand |
| `deleteMessage` | keeps `posted_at`, as it keeps `created_at`. A composing message may be deleted, by its author or a moderator, as any message: the author's way to drop a draft. It also deletes the message's `comment_drafts` rows and sets `draft_length` and `last_append_at` NULL. No count moves for a composing message, which did not stand; an interrupted one leaves the counts as a posted one does |
| `purge` | also deletes every purged message's `comment_drafts` rows; a top-level message's composing replies are purged with its thread |
| `react`, `unreact` | after refusal 2 (`deleted`), `composing` `{ message }` |
| `pin` | after refusal 3 (`deleted`), `composing` `{ message }`. `unpin` is unchanged: a composing message is not pinned, so `not_pinned` covers it |
| `resolve`, `reopen` | after refusal 3 (`deleted`), `composing` `{ message: thread }` |
| `listMessages` | composing and interrupted messages are in the listing, in their places; a composing one's `body` is its draft so far. `thread` may name a composing top-level message (its replies are none) |
| `getMessage` | the record, a composing message's with its draft so far as `body` |
| `listEdits`, `listReactions` | unchanged: a composing message has neither |

`post`'s refusals in batch 2:

| # | Refusal | When |
|---|---------|------|
| 1 | (retry) | as in batch 1, a message `begin` made included |
| 2 | `invalid_argument` `/body` | as in batch 1 |
| 3 | `invalid_argument` `/quotes` `needs thread` | |
| 4 | `invalid_argument` `/blocks/<i>/data` | |
| 5 | `no_message` | `thread` |
| 6 | `not_a_thread` | |
| 7 | `composing` `{ message: thread }` | `thread` names a composing message |
| 8 | `no_message` | `quotes` |
| 9 | `not_in_thread` | |
| 10 | `deleted` | `quotes` names a deleted message |
| 11 | `composing` `{ message: quotes }` | `quotes` names a composing message |
| 12 | `block_not_allowed` | |
| 13 | `ref_not_found` | |

Replying in the thread of an interrupted message, and quoting one, are
allowed, as for a posted one.

### Refusals

New veto codes:

| Veto code | details |
|-----------|---------|
| `composing` | `{ message }`: the composing message an edit, reaction, pin, resolve or reopen named, or the `thread` or `quotes` of a post or begin |
| `not_composing` | `{ message, state }`: `appendDraft`, `finish` or `interrupt` of a message that is not composing, and its state |
| `not_idle` | `{ message, author, lastAppendAt, composeTimeoutMs }` |
| `draft_too_long` | `{ message, length, append, maxBodyLength }` |
| `too_many_drafts` | `{ composing, max }`: how many composing messages the caller has on the instance, and the config's `maxDrafts` |

`edit_window_passed`'s details become `{ message, postedAt,
editWindowMs }` (batch 1 gave `createdAt`).

`not_author` also covers `appendDraft` and `finish` of another's composing
message, with batch 1's details `{ message, author }`.

### Fields

Batch 1's three now count the messages that *stand*:

| Field | Value | Moved by |
|-------|-------|----------|
| `messages` | the standing messages, top-level and replies (the instance column `count`) | post, finish, interrupt (to `interrupted`), deleteMessage and purge of a standing message |
| `lastMessageAt` | the greatest `postedAt` of a standing message | the same |
| `threads` | `{ open, resolved }` of the standing top-level messages | post, finish and interrupt of a top-level message, deleteMessage and purge of a standing top-level one, resolve, reopen |
| `composing` | an array of strings, always present: each subject with a composing message on the instance, once, ordered by the least id of its composing messages, ascending; `[]` while there is none | begin, finish, interrupt, deleteMessage and purge of a composing message |

Nothing moves at `begin` but `composing`, and at `appendDraft` nothing
does. `composing` is volatile like the rest and read at each read:

```sql
SELECT author, MIN(message) AS first FROM <comment_messages>
 WHERE namespace = ? AND schema = ? AND id = ? AND last_append_at IS NOT NULL
 GROUP BY author ORDER BY first
```

through `comment_messages_composing`. It lists an idle message's author
until the schedule interrupts it. A client watching the stream derives it:
a `begin` event adds its author, and a `finish`, `interrupt`,
`deleteMessage` or `purge` event of a composing message removes the
author unless the client holds another composing message of theirs.

`Moves::leaving` and the recount of `last_message_at` read *stands* where
batch 1 reads posted, and the recount reads `MAX(posted_at)` where batch 1
reads `MAX(created_at)`; `Moves::posted_at` is the `posted_at` written,
the call's `now`. `lastReplyAt` reads `MAX(posted_at)` of the thread's
standing replies.

### Migration 3, `drafts`

`BehaviorMigration::new(3, "drafts")`. It creates one table and, in `up`,
adds three columns and two indexes to `comment_messages` and fills one
column
(`ALTER TABLE ... ADD COLUMN`, `CREATE INDEX ... WHERE`, each index named
through `sql.table(name)` as Links names its own), the same on both
dialects:

| `comment_messages` column | Spec | Holds |
|---------------------------|------|-------|
| `draft_length` | integer | the draft's length in code points while composing; NULL otherwise |
| `last_append_at` | integer | the `now` of the message's `begin` or latest `appendDraft` while composing; NULL otherwise |
| `posted_at` | integer | when the message came to stand (the record's `postedAt`); NULL while composing and on a message that never stood |

Exactly while a message is composing, `state = 'composing'`,
`draft_length` is not NULL and `last_append_at` is not NULL; every write
that moves a message out of composing sets both NULL in the same
statement. Existing rows get NULL for both: no message composes before
batch 2. `up` then sets `posted_at = created_at` on every existing row
(`UPDATE <comment_messages> SET posted_at = created_at`): each was posted
by a post, the deleted ones included, so each stood from its
`created_at`. `posted_at` stays nullable. `state` keeps no CHECK.

| Index on `comment_messages` | Columns | Where | Read by |
|-----------------------------|---------|-------|---------|
| `comment_messages_composing` | `(namespace, schema, id, message)` | `last_append_at IS NOT NULL` | the `composing` field |
| `comment_messages_idle` | `(namespace, schema, last_append_at, id, message)` | `last_append_at IS NOT NULL` | the schedule |

Each statement that should use one of them names `last_append_at IS NOT
NULL` itself, so both planners match the index's predicate.

`comment_drafts`, primary key `(namespace, schema, id, message,
chunk_offset)`: `namespace`, `schema`, `id` text not null; `message`
integer not null; `chunk_offset` integer not null (the draft's length
before the chunk: the `offset` of its `draft` signal; `offset` is
reserved in Postgres); `text` text not null (never empty). One row per
non-empty append, so an append inserts a row and never rewrites the
draft; a read concatenates a message's rows in `chunk_offset` order, one
statement for the composing messages of a page:

```sql
SELECT message, text FROM <comment_drafts>
 WHERE namespace = ? AND schema = ? AND id = ? AND message IN (...)
 ORDER BY message, chunk_offset
```

`afterChange` on `Delete` also removes the instance's `comment_drafts`
rows; its composing messages go with its other messages.

### What the generators expose

For the engine client (`engineclientgen`, TypeScript) and the behavior
types (`behaviortypes`, which typegen and rustgen render), from the
declaration's `signals`:

- **Comments' narrowing.** `begin`, `finish` and `interrupt` return the
  message record: add them to `commentsMessageResults`, so each returns
  `<Base>CommentMessage`. `finish`'s `blocks` narrow as `post`'s do
  (dropped when the config allows none). `appendDraft`'s result is as
  declared. The `composing` field is `string[]` (Go `[]string`, Rust
  `Vec<String>`), always present, like `messages` and `threads`; the
  record's `state` is the four-member union.
- **Signal data types.** One type per declared signal, whatever its
  access, from its `paramsSchema`: `<Base><Signal>Signal`, as
  `NoteTypingSignal`, `NoteViewingSignal`, `NoteDraftSignal`, documented
  "The data of Comments' typing signal: <description>". If two behaviors
  of one type declare the same signal name (unless the registry refuses
  that, as it refuses two operations of one name), both are qualified,
  `<Base><Behavior><Signal>Signal`.
- **`<Base>Signals`.** By behavior, by signal name, the data type:
  `{ Comments: { typing: NoteTypingSignal; viewing: NoteViewingSignal;
  draft: NoteDraftSignal } }`, beside `<Base>Vetoes`. A behavior with no
  signal has no member.
- **`<Base>SignalMessage`.** The engine client's stream signal message
  (whatever the signals work names it, with `from`, `at` and the
  instance) with `behavior`, `name` and `data` narrowed to a union over
  every declared signal of the type, discriminated by `behavior` and
  `name`; and `is<Base>Signal(message, behavior?, name?)`, a guard that
  checks the message's schema, then the behavior and name when given, as
  `is<Base>Veto` checks a veto's.
- **Send helpers.** On `<Schema>Client`, one method per signal whose
  access is `read` or `write`, none for `operation`:
  `send<Signal>(id, data, options?)`, calling the engine client's signal
  send with the schema, the id, the behavior and the name, and resolving
  to `void`; `data` is optional when its schema requires no member:
  `sendTyping(id, data?, options?)`, `sendViewing(id, data?, options?)`.
  A send helper whose name an operation of the type already takes is
  `send<Behavior><Signal>`. The doc comment is the signal's description.
- **Draft following.** The client's docs for `getMessage` and
  `NoteDraftSignal` point at "What a reader does with draft" above; a
  helper that holds drafts is not generated.
- Go and Rust types render behavior fields only: they gain `composing`
  and nothing for signals.

## Batch 3: read state, follows, inbox and deliveries

Batch 3 is D77's step 3 on Comments' side: each caller's read markers
(`markRead`, the `unread` field), follows (`follow`, and who follows
without asking), deliveries (one row per message and subject that should
hear of it), and three namespace operations (D77 primitive 3): `inbox`,
`claimDeliveries` and `settleDeliveries`. The engine's side of namespace
operations, `"scope": "namespace"`, its context, `readable()`, the
policy's question, the route and the MCP tool, is the namespace-operations
work's; this section says only what Comments declares and does with them.

### Terms

- A message's *stand* is its place in the order the conversation's
  messages came to stand: the conversation's `last_stand + 1` (which
  becomes `last_stand`) at the call that makes it stand, a `post`, a
  `finish`, or an `interrupt` to `interrupted`. It never changes, and a
  message deleted after it stood keeps it. A message deleted while
  composing, or interrupted with no text, never has one. Markers count in
  stands, not ids: a streamed reply takes its id at `begin` and its stand
  at `finish`, so one that began before a message a reader has read, and
  finished after, is still unread. Stands are numbered per instance, as
  ids are.
- A subject's *conversation marker* is its `comment_reads.up_to` on the
  instance, a stand; a subject with no row has none. Its *thread marker*
  on a thread is its `comment_follows.read_up_to` for that thread; with no
  row, none.
- Subject S *has read* a standing message X when X's marker for S exists
  and is >= `X.stand`: the conversation marker for a top-level X, the
  thread marker of X's thread for a reply. A message with no marker is
  unread.
- *Others'* messages are those whose `author` is not the caller's subject.
- S *follows* thread T when S's `comment_follows` row for T has
  `followed = 1`.
- A *delivery* is a `comment_deliveries` row: one message, one subject,
  its reasons (`mention`, `reply`), its state.
- A delivery is *due* when `due_at IS NOT NULL AND due_at <= now`: a
  pending one at once; a failed one once its next attempt comes; a claimed
  one once its claim lapses. A sent or dropped one never is.
- The *deliverable schemas* of a call are the schemas in `readable()`
  whose Comments config names a `deliverPermission` that `cx.can()` grants
  the caller ("claimDeliveries").
- *Comes to stand*: the event at `post`, `finish`, and `interrupt` to
  `interrupted`, after which "Deliveries" runs.
- Lengths in code points, as batch 2's.

### Config

Batch 3 adds four members, every one optional:

| Member | Type | When absent | Meaning |
|--------|------|-------------|---------|
| `deliverPermission` | string, `minLength` 1 | nobody claims the schema's deliveries | what `claimDeliveries` and `settleDeliveries` need of the caller, for the deliveries of this schema's messages, beside the policy's `Comments.claimDeliveries` and `Comments.settleDeliveries`. Deliveries are recorded either way: the inbox reads them |
| `maxAttempts` | integer 1..20 | 5 | how many claims a delivery gets: a failure settled on, or a claim that lapses at, attempt `maxAttempts` drops it |
| `retryBackoffMs` | integer 1000..3600000 | 30000 | a failed delivery is due again `min(retryBackoffMs * 2^(attempts - 1), 3600000)` ms after the settle |
| `deliveryRetentionMs` | integer 86400000..31536000000 | 2592000000 (30 days) | how long a delivery is kept from its `created_at`, whatever its state but a live claim; the inbox's mention items reach back as far ("The schedule prune") |

Each namespace operation reads the config of a delivery's own schema
(`cx.config(schema)`, the `CommentsConfig` its live version parsed; a
`NamespaceCx` has no `schemas()`): a namespace has no single config. A config change applies to the next
call; a delivery already failed keeps the `due_at` its settle gave it.

### The record and listMessages

Amends batch 2's record table:

| Member | Batch 3 |
|--------|---------|
| `stand` | new, integer, right after `postedAt`: the message's stand ("Terms"). Present exactly when `postedAt` is; a message that stood before migration 4 has its id as its stand ("Migration 4") |

`listMessages`' result gains `readUpTo`, after `after` and before `root`:
the caller's conversation marker without `thread`, its thread marker on
`thread` with it; absent when it has none. A client shows a message as
unread when it is another's, stands, and its `stand` is above `readUpTo`,
and draws no line by ids.

### Who follows

A follow row is the subject's state on one thread, whether it follows or
not: `followed`, and its thread marker `read_up_to`. Rows are written by:

| Write | When | Row |
|-------|------|-----|
| the author's | a message comes to stand | thread T = the message's `thread`, or the message itself when top-level: `followed = 1`, `read_up_to` = the greater of the row's and the message's stand. For a top-level message, also its conversation marker: `up_to` = the greater of the row's and the stand (the `comment_reads` row inserted when absent) |
| a mention's | a message comes to stand with `mentions`, or an edit adds a mention ("Batch 1 and 2 operations, amended") | for each mentioned subject that is not the author and does not start with `role:`, thread T as above: absent, inserted with `followed = 1` and `read_up_to = W`; `followed = 0`, set to 1 and `read_up_to` = the greater of the row's and W; `followed = 1`, unchanged. W is the message's stand for a top-level message (the thread's replies all come later) and the stand - 1 for a reply (so the reply that mentions it counts in the thread item) |
| `follow` | the call | below |
| `markRead` with `thread` | the call | below; inserts `followed = 0` when absent |

So a subject follows each thread it posts in and each it is mentioned in,
again after `follow({ on: false })`; `begin` makes nobody follow, since a
message deleted while composing never was in the thread; an `interrupt` to
`interrupted` makes its author follow, the runner never. A `role:<name>`
mention follows nothing: a group is not a reader, and following would send
the whole group every later reply.

### Deliveries

When message X comes to stand, after its own writes and its stand, in the
same call:

1. *Followers*: the subjects that follow X's thread, read now, before
   step 4's writes. A top-level X has none.
2. *Mentioned*: X's `mentions` as written (`[]` for an `interrupt`).
3. *Recipients*: (Mentioned union Followers) minus X's author. For each, a
   `comment_deliveries` row: `mention` = 1 when it is in Mentioned,
   `reply` = 1 when it is in Followers, `state = 'pending'`, `due_at =
   now`, `attempts = 0`, `created_at = now`, every other column NULL. No
   row exists yet for X, so each is an insert. A `role:<name>` subject gets
   its row as given; the notifier expands it.
4. The follow and marker writes of "Who follows".

`reasons`, wherever a delivery is returned, is `["mention"]`,
`["reply"]` or `["mention", "reply"]` from the two columns: one row per
subject, with each reason that holds, never two rows.

Nothing at `begin` or `appendDraft`. An `interrupt` to `deleted` makes no
delivery. An interrupted reply's followers hear of it as of any reply: it
stands, its text is what its author wrote, and a reader should know the
agent stopped. A retry of a `post` (refusal 1) writes nothing, deliveries
included.

The states, and what each column holds:

| `state` | `due_at` | `token`, `claimed_by` | `attempts` | `error` | `settled_at` |
|---------|----------|-----------------------|------------|---------|--------------|
| `pending` | `created_at`: due | NULL | 0 | NULL | NULL |
| `claimed` | the claim's `expiresAt` | the claim's token and the claimer's subject | including this claim | the last failure's, or NULL | the last settle's, or NULL |
| `failed` | the next attempt | NULL, the last claimer | as at the claim | the settle's | the settle's `now` |
| `sent` | NULL | NULL, the last claimer | as at the claim | NULL | the settle's `now` |
| `dropped` | NULL | NULL, the last claimer, or NULL | as it was | the settle's, or the claim's reason (`message_gone`, `read`, `claim_lapsed`) | the `now` it was dropped |

The engine sends nothing (D77). The delivery row is also the inbox's
record of a mention ("inbox"), so it is kept, sent or not, until
`deliveryRetentionMs` passes.

A delivery is recorded for every subject X mentions and every follower,
whether or not that subject may read the schema: Comments asks the policy
nothing about a recipient, since only the caller is a principal here, and
a `role:<name>` subject is not one at all. `claimDeliveries` returns the
message's body, up to 4000 code points ("claimDeliveries"), to the
claimer, whose `readable()` holds the schema; it says nothing about the
recipient. So a notifier must check that the recipient may read the
instance before it sends the body anywhere: read the instance as the
recipient (`instances.get` with the recipient's principal, its subject
and permissions from the deployment's identity store, which asks the
policy as the recipient's own read would), or ask the deployment's
policy the same question with the recipient's permissions. For a
`role:<name>` subject it checks each member it expands to. A recipient
who may not read it gets no body: the notifier settles the delivery
`dropped`, or sends a notice with no text that links to the instance,
where a sign-in decides.

### Operations

`markRead` and `follow` are `"writes": true, "beside": true, "log":
false, "asks": "read"`: they append no event and move nothing a read of
another caller returns, and since they write only the caller's own state
the policy is asked `read` with the operation `markRead` or `follow`, so a
caller who may read the instance may call them under `permissionPolicy`.

#### `markRead({ upTo, thread? })`

Result `{ readUpTo, unread }`: the marker after the call, and how many of
others' standing messages of the listing stand above it. No event.

| # | Refusal | When |
|---|---------|------|
| 1 | `no_message` | `thread` names no present message |
| 2 | `not_a_thread` `{ message, thread }` | `thread` names a reply |

`upTo` is a bound, as `before` and `after` are: an id that names no
message is never refused. `thread` may name a composing or deleted
top-level message.

Writes: S is the greatest stand among the listing's messages (top-level
without `thread`, the thread's replies with it) whose id is at most
`upTo`:

```sql
SELECT MAX(stand) AS s FROM <comment_messages>
 WHERE namespace = ? AND schema = ? AND id = ? AND thread IS NULL   -- or: AND thread = ?
   AND message <= ? AND stand IS NOT NULL
```

`stand IS NOT NULL` alone, not the state: a message deleted after it stood
keeps its stand, and counts in no unread. S is 0 when NULL. Without
`thread`: the caller's `comment_reads` row gets `up_to` = the greater of
its `up_to` and S, `read_at = now`; inserted with `up_to = S` when absent,
which is how a caller starts to count a conversation's unread. With
`thread`: the caller's `comment_follows` row for it gets `read_up_to` = the
greater of its own and S, `updated_at = now`; inserted with `followed = 0`
and `read_up_to = S` when absent. The marker never moves back. Neither
dialect's `MAX(a, b)` is the other's (`GREATEST` in Postgres): compare in
the handler, or with `CASE`.

`unread` in the result is the `unread` field's statement ("Fields") for
the conversation, and for a thread:

```sql
SELECT COUNT(*) AS unread FROM <comment_messages>
 WHERE namespace = ? AND schema = ? AND id = ? AND thread = ?
   AND stand IS NOT NULL AND stand > ? AND state IN ('posted', 'interrupted') AND author <> ?
```

A markRead that moves a marker past a mention reads that mention, and its
inbox item is done. It cancels no delivery itself; the next claim drops a
pending one its subject has read ("claimDeliveries").

#### `follow({ thread, on })`

Result `{ thread, following }`: `thread` as given, `following` whether the
caller follows it after the call. No event.

| # | Refusal | When |
|---|---------|------|
| 1 | `no_message` | `thread` names no present message |
| 2 | `not_a_thread` `{ message, thread }` | `thread` names a reply |

A composing or deleted top-level message may be followed. Writes, with L
the conversation's `last_stand` (0 when it has no row):

| `on` | The caller's row | Writes |
|------|------------------|--------|
| true | absent | insert `followed = 1`, `read_up_to = L`, `updated_at = now` |
| true | `followed = 0` | `followed = 1`, `read_up_to` = the greater of its own and L, `updated_at = now` |
| true | `followed = 1` | nothing |
| false | `followed = 1` | `followed = 0`, `updated_at = now`; `read_up_to` stays |
| false | absent or `followed = 0` | nothing |

So a follow counts the thread's replies from the conversation's latest
stand: what came before it is not new to the caller. Neither is refused
for asking what already holds.

### Namespace operations

`inbox` is `"scope": "namespace"` and reads; `claimDeliveries` and
`settleDeliveries` are `"scope": "namespace", "writes": true`. None is
beside or logged: a namespace operation appends no event (D77 primitive
3). The engine asks the policy `{ principal, action, namespace, behavior:
"Comments", operation }` before the behavior's checks; `permissionPolicy`
lets `inbox` through and asks `Comments.claimDeliveries` and
`Comments.settleDeliveries` of the other two. Every statement joins
`sql.readable()`, written `<readable>` below, the schemas that compose
Comments and that the caller may read (column `schema`), or reads its
schemas into a list it binds; a row of another schema never leaves a
statement. `cx.now()` is one value for the call. A schema's config
(`deliverPermission`, `maxAttempts`, `retryBackoffMs`,
`deliveryRetentionMs`) comes from `cx.configs()`, each composing schema's
live config, or `cx.config(schema)`. The two writing operations run under
the namespace's writer, so they serialize with posts and finishes.

#### `inbox({ kinds?, unread?, limit?, cursor? })`

Read-only. Result `{ items, next }`, a page in the inbox's order, newest
first. An item is `{ kind, schema, instanceId, message, thread?, author,
postedAt, excerpt, unread }`, members in that order:

| `kind` | One per | `message` | `thread` | `unread` |
|--------|---------|-----------|----------|----------|
| `mention` | delivery of the caller's subject with `mention = 1` whose message stands | the message that mentions the caller | when it is a reply, its thread | 1 while the caller has not read the message ("Terms"), 0 once it has |
| `thread` | thread the caller follows that has a standing reply of another | the thread's standing reply of another with the greatest stand | the thread followed | how many standing replies of others in it stand above the caller's thread marker |
| `conversation` | conversation the caller has a conversation marker on that has a standing top-level message of another | its standing top-level message of another with the greatest stand | absent | how many standing top-level messages of others stand above the marker |

`author` and `postedAt` are the item's message's. An item whose `unread`
is 0 is *done*. One message can be in two items: a reply that mentions
the caller in a thread it follows is a mention and its thread's latest
reply, as Slack lists it in both its activity and its threads.

A mention item is done once the caller has read the message: once
`markRead` moves the conversation marker (a top-level message) or the
thread marker (a reply) past it, or the caller posts after it where that
moves the marker ("Who follows"). The inbox clears nothing else, and
nothing deletes a done item; `unread: true` hides it. A mention of a group
the caller is in is not in its inbox: a principal carries no roles, so the
engine cannot tell. Mention items reach back `deliveryRetentionMs`, the
age of the delivery rows they read; thread and conversation items have no
horizon.

`excerpt`: the message's body with each maximal run of white space
(Unicode `White_Space`, Rust's `char::is_whitespace`) replaced by one
space U+0020, and leading and trailing space removed; when that is longer
than 200 code points, its first 199 code points, a trailing space removed,
then `…` (U+2026).

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` `/cursor` `is not a cursor this operation returned` | the cursor does not decode as below |

Order and paging. Each item has the key `(postedAt, rank, schema,
instanceId, message)`, `rank` 3 for `mention`, 2 for `thread`, 1 for
`conversation`, and the inbox lists items in descending key order: the
newest first, and at one `postedAt`, mentions before threads before
conversations. Within one kind `(schema, instanceId, message)` is unique,
since a thread item's latest reply names its thread and a conversation
item's latest message its conversation.

- `limit` 1..100, 50 when absent.
- One statement per kind asked (`kinds`, all three when absent), each
  ordered `posted_at DESC, schema DESC, id DESC, message DESC` with `LIMIT
  limit + 1`, after the cursor as below. The handler merges the lists by
  `(postedAt, rank)` descending, each list keeping its own order (so the
  handler never compares text: the database's order of `schema` and `id`
  holds), and takes the first `limit`.
- `next`: when the lists held more than `limit` items in all, the cursor of
  the last item taken; else null.
- The cursor is the item's key as the compact JSON array `[postedAt, kind,
  schema, instanceId, message]` (`kind` its name), in unpadded base64url
  as the paging module writes it. It decodes to exactly that: an array of
  five, an integer, one of the three kinds, two strings, an integer >= 1;
  anything else is refusal 1.
- After cursor `(p, kc, s, i, m)` with `rc` the rank of `kc`, the statement
  of the kind of rank `r` adds: `posted_at <= p` when `r < rc`;
  `posted_at < p` when `r > rc`; `(posted_at, schema, id, message) < (p,
  s, i, m)` when `r = rc`.
- `unread: true` adds `unread > 0`, `unread: false` adds `unread = 0`, in
  the statement, so a page is never short for it.

An item moves up when a newer message comes for it, so a client that pages
on while the inbox changes may miss an item that moved above its cursor;
it reads the first page again to catch up, as a feed does.

The three statements (`?` bound in order; the outer `WHERE` takes the
`unread` filter and the cursor's condition):

```sql
-- mention
SELECT * FROM (
  SELECT d.schema, d.id, d.message, m.thread, m.author, m.posted_at, m.body,
         CASE WHEN m.stand > COALESCE(CASE WHEN m.thread IS NULL THEN r.up_to ELSE f.read_up_to END, 0)
              THEN 1 ELSE 0 END AS unread
    FROM <comment_deliveries> d
    JOIN <readable> k ON k.schema = d.schema
    JOIN <comment_messages> m
      ON m.namespace = d.namespace AND m.schema = d.schema AND m.id = d.id AND m.message = d.message
    LEFT JOIN <comment_reads> r
      ON r.namespace = d.namespace AND r.schema = d.schema AND r.id = d.id AND r.subject = d.subject
    LEFT JOIN <comment_follows> f
      ON f.namespace = d.namespace AND f.schema = d.schema AND f.id = d.id
     AND f.thread = m.thread AND f.subject = d.subject
   WHERE d.namespace = ? AND d.subject = ? AND d.mention = 1
     AND m.state IN ('posted', 'interrupted') AND m.stand IS NOT NULL
) x
 WHERE ...
 ORDER BY x.posted_at DESC, x.schema DESC, x.id DESC, x.message DESC
 LIMIT ?

-- thread
SELECT * FROM (
  SELECT f.schema, f.id, f.thread, m.message, m.author, m.posted_at, m.body,
         (SELECT COUNT(*) FROM <comment_messages> u
           WHERE u.namespace = f.namespace AND u.schema = f.schema AND u.id = f.id
             AND u.thread = f.thread AND u.stand IS NOT NULL AND u.stand > f.read_up_to
             AND u.state IN ('posted', 'interrupted') AND u.author <> f.subject) AS unread
    FROM <comment_follows> f
    JOIN <readable> k ON k.schema = f.schema
    JOIN <comment_messages> m
      ON m.namespace = f.namespace AND m.schema = f.schema AND m.id = f.id
     AND m.message = (SELECT l.message FROM <comment_messages> l
                       WHERE l.namespace = f.namespace AND l.schema = f.schema AND l.id = f.id
                         AND l.thread = f.thread AND l.stand IS NOT NULL
                         AND l.state IN ('posted', 'interrupted') AND l.author <> f.subject
                       ORDER BY l.stand DESC LIMIT 1)
   WHERE f.namespace = ? AND f.subject = ? AND f.followed = 1
) x
 WHERE ...
 ORDER BY x.posted_at DESC, x.schema DESC, x.id DESC, x.message DESC
 LIMIT ?

-- conversation: the thread statement over <comment_reads> r, with
-- `thread IS NULL` for `thread = f.thread`, `r.up_to` for `f.read_up_to`,
-- and no followed condition; it selects no thread.
```

`m.thread` is NULL for a top-level message, so the mention statement's
follow join finds nothing for it, and its `CASE` reads the conversation
marker. The thread and conversation statements cost one ordered probe of
`comment_messages_stand` and one count per row of the caller's, so an
inbox reads as many rows as the caller follows threads and has read
conversations in the namespace; a caller with tens of thousands of them is
past what batch 3 holds ("Left").

#### `claimDeliveries({ subjects?, prefix?, limit?, ttlMs? })`

Writes. Claims due deliveries under one new token. Result `{ token,
expiresAt, items }`; `token` and `expiresAt` are null when `items` is
empty. An item is `{ id, schema, instanceId, message, thread?, subject,
reasons, author, via?, state, postedAt, body, truncated, attempt }`,
members in that order: `state` the message's (`posted` or `interrupted`),
`body` its body as it is now (an edit since the delivery was made shows),
its first 4000 code points, `truncated` whether it was longer, `attempt`
the delivery's `attempts` after this claim (1 for the first).

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` `/prefix` `must not be given with subjects` | both are given |
| 2 | `not_configured` `{ permission: "deliverPermission" }` | no schema of `readable()` has a Comments config naming `deliverPermission` |
| 3 | `forbidden` | some do, and `cx.can()` grants the caller none of the permissions they name; message `"<subject> may not claimDeliveries in namespace <namespace>: it needs permission <permissions>"`, the distinct permissions named, sorted, joined with `", "` |

`limit` 1..100, 20 when absent; `ttlMs` 1000..600000, 60000 when absent.

The deliverable schemas: for each schema of `readable()` (`SELECT schema
FROM <readable>`), its config's `deliverPermission`, and `cx.can()` asked
once per distinct permission. Refusals 2 and 3 read them; the claim reads
only them. `subjects` keeps deliveries whose subject is one of them;
`prefix` those whose subject starts with it, compared as code points
(`substr(subject, 1, ?) = ?`, bound with the prefix's length in code
points and the prefix: both dialects' `substr` counts characters, and
neither needs `LIKE`'s escapes).

Then, with `token` a new UUID from `cx.uuids()` and `expiresAt = now +
ttlMs`, it reads due deliveries in the order `(due_at, schema, id,
message, subject)`, a page at a time, through `comment_deliveries_due`:

```sql
SELECT d.schema, d.id, d.message, d.subject, d.mention, d.reply, d.state, d.attempts, d.due_at,
       m.thread, m.author, m.via, m.state AS message_state, m.posted_at, m.stand, m.body,
       CASE WHEN m.thread IS NULL THEN r.up_to ELSE f.read_up_to END AS marker
  FROM <comment_deliveries> d
  LEFT JOIN <comment_messages> m
    ON m.namespace = d.namespace AND m.schema = d.schema AND m.id = d.id AND m.message = d.message
  LEFT JOIN <comment_reads> r
    ON r.namespace = d.namespace AND r.schema = d.schema AND r.id = d.id AND r.subject = d.subject
  LEFT JOIN <comment_follows> f
    ON f.namespace = d.namespace AND f.schema = d.schema AND f.id = d.id
   AND f.thread = m.thread AND f.subject = d.subject
 WHERE d.namespace = ? AND d.due_at IS NOT NULL AND d.due_at <= ?      -- now
   AND d.schema IN (?, ...)                                            -- the deliverable schemas
   [AND d.subject IN (?, ...)] [AND substr(d.subject, 1, ?) = ?]
   [AND (d.due_at, d.schema, d.id, d.message, d.subject) > (?, ?, ?, ?, ?)]
 ORDER BY d.due_at, d.schema, d.id, d.message, d.subject
 LIMIT ?
```

Each row, in order, is one of:

| Case | When | Writes | Returned |
|------|------|--------|----------|
| gone | `m.state` is NULL (purged) or not `posted` or `interrupted` | `dropped`, `error = 'message_gone'` | no |
| read | the marker is not NULL and `marker >= m.stand` (the subject has read it) | `dropped`, `error = 'read'` | no |
| spent | `state = 'claimed'` (its claim lapsed) and `attempts >= maxAttempts` of its schema | `dropped`, `error = 'claim_lapsed'` | no |
| claimed | otherwise | `state = 'claimed'`, `token`, `claimed_by` = the caller's subject, `due_at = expiresAt`, `attempts + 1` | yes |

A drop sets `due_at`, `token` NULL and `settled_at = now`. Every write is
`UPDATE ... WHERE <key> AND due_at = ?`, bound with the `due_at` the row
was read with; one that updates no row was taken by a concurrent claim and
is skipped. The engine runs a writing namespace operation under the
namespace's writer, as it runs an instance's writes; the guard keeps two
claims apart even if it did not. The claim stops at `limit` claimed, at a
page shorter than asked, or at 1000 rows read, whichever is first, and
returns what it claimed in the order read. A due delivery whose subject
has read its message is the notifier's to skip, so it never sends a person
what they already saw.

The delivery `id` is the compact JSON array `[schema, instanceId,
message, subject]`, as `serde_json::to_string` writes it, in unpadded
base64url: the same at every claim, and decoded, never looked up, by
`settleDeliveries`.

#### `settleDeliveries({ token, ids, outcome, error? })`

Writes. Result `{ items }`: per id, in the order given, `{ id, state,
attempts, nextAttemptAt? }`, `state` the delivery's after the call and
`nextAttemptAt` its `due_at` when failed. All the ids settle, or none.

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` `/error` `must not be given with outcome sent` | |
| 2 | `not_configured` `{ permission: "deliverPermission" }` | as claimDeliveries' |
| 3 | `forbidden` | as claimDeliveries', with `settleDeliveries` in the message |
| 4 | `unknown_delivery` `{ ids }` | each id that does not decode to `[string, string, integer >= 1, string]`, or names no row of the namespace, or a row of a schema not deliverable for the caller; `ids` every such id, in the order given |
| 5 | `token_stale` `{ token }` | no row of the namespace holds `token`: each it claimed was settled, or claimed again after the claim lapsed, or it was never one |
| 6 | `not_claimed` `{ token, ids }` | each id whose row does not hold `token`: pending, settled, or claimed under another; `ids` every such id, in the order given |

A row that still holds the token after its claim lapsed settles: nobody
else took it, so nobody else sent it. Refusal 5 reads
`comment_deliveries_token`:

```sql
SELECT 1 FROM <comment_deliveries> WHERE namespace = ? AND token = ? LIMIT 1
```

Writes, for each row, by `outcome`, with `attempts` as the row holds it
(the claim counted this attempt) and M the `maxAttempts`, B the
`retryBackoffMs` of the row's schema:

| `outcome` | Writes |
|-----------|--------|
| `sent` | `state = 'sent'`, `due_at`, `token`, `error` NULL, `settled_at = now` |
| `failed`, `attempts < M` | `state = 'failed'`, `due_at = now + min(B * 2^(attempts - 1), 3600000)`, `token` NULL, `error` = `error` or NULL, `settled_at = now` |
| `failed`, `attempts >= M` | as `dropped` |
| `dropped` | `state = 'dropped'`, `due_at`, `token` NULL, `error` = `error` or NULL, `settled_at = now` |

`claimed_by` stays: the last claimer. The backoff is computed without
overflow (`2^(attempts - 1)` with `attempts` at most 20 fits an `i64`).

### Batch 1 and 2 operations, amended

| Operation | Batch 3 |
|-----------|---------|
| `post` | after its writes: the message's stand (`stand`, `last_stand`), then "Deliveries" (followers, mentions) and "Who follows" (the author; mentioned subjects). The record carries `stand`. A retry writes nothing |
| `begin`, `appendDraft` | unchanged: no stand, no delivery, no follow |
| `finish` | as `post`: stand, deliveries for its `mentions` and the thread's followers, follows |
| `interrupt` | to `interrupted`: stand, deliveries for the thread's followers (no mentions: an interrupted message has none), the author follows and its marker moves. To `deleted`: nothing new |
| `edit` | when `mentions` is given, with O the message's mentions before and N after, A its author: for each subject in N not in O, not A: no row for the message, insert one as "Deliveries" does with `mention = 1`, `reply = 0`; a row, `mention = 1`, and when it is `sent` or `dropped` it returns to `pending` (`due_at = now`, `created_at = now`, `attempts = 0`, `token`, `claimed_by`, `error`, `settled_at` NULL), so a subject told of the reply hears of the mention; `pending`, `failed` or `claimed`, nothing else moves. Then the mention's follow ("Who follows", W from the message's stand). For each subject in O not in N: its row, when `pending` or `failed`, gets `mention = 0` and is deleted when `reply = 0`; a `claimed`, `sent` or `dropped` row is left (a claimer may settle it, or the subject was told). A moderator's edit is the same; recipients exclude the author, not the editor |
| `deleteMessage` | deletes the message's deliveries but the claimed ones, which stay for their claimer's settle (and are dropped as `message_gone` if their claim lapses). Its `stand` stays; follows and markers stay |
| `purge` | deletes every purged message's deliveries but the claimed ones; for a top-level message, every `comment_follows` row of its thread. Markers stay: they count stands, which are not reused |
| `listMessages` | the result's `readUpTo` ("The record and listMessages") |
| `getMessage`, `react`, `unreact`, `pin`, `unpin`, `resolve`, `reopen` | unchanged, but the record's `stand` |

`edit` refusals are unchanged; its deliveries are written after the edit's
own writes. Batch 1's rule that `mentions` are not checked holds: an
unknown subject's delivery is the notifier's to drop.

### Refusals

New veto codes:

| Veto code | details |
|-----------|---------|
| `unknown_delivery` | `{ ids }` |
| `token_stale` | `{ token }` |
| `not_claimed` | `{ token, ids }` |

`not_configured` also covers `claimDeliveries` and `settleDeliveries`,
details `{ permission: "deliverPermission" }`. `no_message` and
`not_a_thread` also cover `markRead`'s and `follow`'s `thread`. `forbidden`
(403) for a namespace operation names the namespace in place of the
instance.

### Fields

| Field | Value | Moved by |
|-------|-------|----------|
| `unread` | integer, always present: the caller's count of standing top-level messages of others whose stand is above the caller's conversation marker; 0 while the caller has none | post, finish and interrupt of another's top-level message; deleteMessage and purge of one; markRead; the caller's own top-level stand |

It is the caller's, the only field that differs by who reads: the view's
principal (`view.principal().subject`). Replies are not counted: a
conversation that counted every reply in every thread would read unread
for whoever opened it once, and a thread is read in the inbox, by its
follower, or by `listMessages` with `thread` and its `readUpTo`. A
conversation the caller has never marked read, nor posted in top-level,
counts 0: it is new to them, not unread, and the namespace's every
instance is not unread for every reader. A mention reaches them anyway.

```sql
SELECT COUNT(*) AS unread
  FROM <comment_messages> u
  JOIN <comment_reads> r
    ON r.namespace = u.namespace AND r.schema = u.schema AND r.id = u.id AND r.subject = ?
 WHERE u.namespace = ? AND u.schema = ? AND u.id = ? AND u.thread IS NULL
   AND u.stand IS NOT NULL AND u.stand > r.up_to
   AND u.state IN ('posted', 'interrupted') AND u.author <> ?
```

A client watching the stream keeps it from its own `readUpTo` and each
event's record: a `post`, `finish` or `interrupt` event of another's
top-level message whose record stands adds one, a `deleteMessage` or
`purge` of one whose stand is above the marker takes one away.

### The schedule `prune`

Comments declares a second schedule, `prune`, every 3600000 ms
(`Every::Ms`). Each run, for its namespace and schema, removes the
deliveries made more than `deliveryRetentionMs` ago that no live claim
holds, oldest first, through `comment_deliveries_created`, in batches. A
batch reads the `created_at` of the 1000th oldest of them and deletes those
made up to then, or every one of them when there are fewer:

```sql
SELECT created_at FROM <comment_deliveries>
 WHERE namespace = ? AND schema = ? AND created_at < ?          -- now - deliveryRetentionMs
   AND (state <> 'claimed' OR due_at <= ?)                      -- now
 ORDER BY created_at LIMIT 1 OFFSET 999;

DELETE FROM <comment_deliveries>
 WHERE namespace = ? AND schema = ? AND created_at < ?
   AND (state <> 'claimed' OR due_at <= ?)
   AND created_at <= ?;                                         -- the batch's last; absent when fewer than 1000
```

A batch takes 1000 deliveries, more when others share the last one's
millisecond. The run goes on while a batch takes 1000 or more, up to 20
batches: at least 20000 deliveries a run, 480000 a day. A schema that makes
more than that keeps a backlog the next runs work through, and its oldest
deliveries go first.

A delivery lives `deliveryRetentionMs` whatever its state: a deployment
that runs no notifier still records them for the inbox, and they must not
grow without end; a notification a month late is no use; and a mention
older than the window falls out of the inbox. Settled deliveries are
not pruned sooner, since the inbox's mention items are the same rows.
Markers and follows have no retention: a row per reader per conversation
or thread they touched, removed with its instance or its purged thread.

### Migration 4, `inbox`

`BehaviorMigration::new(4, "inbox")`, the same on both dialects. It adds
two columns and an index to existing tables and creates three:

| Table | Column | Spec | Holds |
|-------|--------|------|-------|
| `comment_messages` | `stand` | integer | the message's stand; NULL until it stands, kept after |
| `comment_conversations` | `last_stand` | integer not null default 0 | the greatest stand given |

Index `comment_messages_stand` on `comment_messages (namespace, schema, id,
thread, stand)` where `stand IS NOT NULL`: the latest standing message of
a listing, and the counts above a marker. Statements that should use it say
`stand IS NOT NULL`, as batch 2's say `last_append_at IS NOT NULL`.

`up`: `UPDATE <comment_messages> SET stand = message WHERE posted_at IS
NOT NULL`, then `UPDATE <comment_conversations> SET last_stand =
last_message`. No marker exists before migration 4, so any numbering that
keeps every new stand above every old one serves; the id is one, and a
message that stood keeps its id as its stand.

`comment_reads`, primary key `(namespace, schema, id, subject)`: a
subject's conversation marker.

| Column | Spec | Holds |
|--------|------|-------|
| `namespace`, `schema`, `id` | text not null | the instance |
| `subject` | text not null | |
| `up_to` | integer not null | a stand |
| `read_at` | integer not null | the `now` of the latest write |

Index `comment_reads_subject` on `(namespace, subject)`: the inbox's
conversations.

`comment_follows`, primary key `(namespace, schema, id, thread, subject)`:
a subject's state on a thread.

| Column | Spec | Holds |
|--------|------|-------|
| `namespace`, `schema`, `id` | text not null | |
| `thread` | integer not null | the thread's top-level message |
| `subject` | text not null | |
| `followed` | integer not null | 1 while it follows, 0 after `follow({ on: false })` or for a row `markRead` made |
| `read_up_to` | integer not null | its thread marker, a stand |
| `updated_at` | integer not null | |

`followed`, not `following`, which is a keyword of both dialects' window
frames. The primary key serves a thread's followers. Index
`comment_follows_subject` on `(namespace, subject)` where `followed = 1`:
the inbox's threads.

`comment_deliveries`, primary key `(namespace, schema, id, message,
subject)`:

| Column | Spec | Holds |
|--------|------|-------|
| `namespace`, `schema`, `id` | text not null | |
| `message` | integer not null | |
| `subject` | text not null | as the mention or the follow gave it |
| `mention` | integer not null default 0 | 1 when a reason is the mention |
| `reply` | integer not null default 0 | 1 when a reason is the follow |
| `state` | text not null | `pending`, `claimed`, `failed`, `sent`, `dropped`; no CHECK |
| `due_at` | integer | "Deliveries"' table; NULL when sent or dropped |
| `token` | text | the claim's, while claimed |
| `claimed_by` | text | the last claimer's subject |
| `attempts` | integer not null default 0 | claims so far |
| `error` | text | |
| `created_at` | integer not null | when made, or returned to pending by an edit |
| `settled_at` | integer | |

| Index on `comment_deliveries` | Columns | Where | Read by |
|-------------------------------|---------|-------|---------|
| `comment_deliveries_due` | `(namespace, due_at, schema, id, message, subject)` | `due_at IS NOT NULL` | `claimDeliveries` |
| `comment_deliveries_mentions` | `(namespace, subject)` | `mention = 1` | `inbox`'s mentions |
| `comment_deliveries_token` | `(namespace, token)` | `token IS NOT NULL` | `settleDeliveries`' refusal 5 |
| `comment_deliveries_created` | `(namespace, schema, created_at)` | | the schedule `prune` |

The primary key serves a message's deliveries (`edit`, `deleteMessage`,
`purge`). `claimDeliveries` with `prefix` or `subjects` filters the due
index's rows, so a dispatcher that claims `agent:` reads past people's due
deliveries while no notifier claims them; past 1000 rows read it returns
what it has.

### Deleting the instance

`afterChange` on `Delete` also removes the instance's `comment_reads`,
`comment_follows` and `comment_deliveries` rows, claimed deliveries
included: a settle of one then meets `unknown_delivery`. The
`comment_conversations` row keeps `last_stand` with `last_message`, so a
re-created instance numbers stands on.

### What the generators expose

For `behaviortypes` (typegen, rustgen) and `engineclientgen`:

- **Fields.** `unread` is `number` (Go `int64`, Rust `i64`), always
  present, like `messages`. Its doc says it is the reader's.
- **The record.** `stand?: number` after `postedAt` on the message record
  (`<Base>CommentMessage`), optional as `postedAt` is. `listMessages`'
  result gains `readUpTo?: number`.
- **`markRead` and `follow`.** Instance operations with their declared
  results, on `<Schema>Client` as every instance operation; not in
  `commentsMessageResults`.
- **Namespace operations.** An operation of `"scope": "namespace"` is in
  no schema's `<Base>Operations`, `<Base>InstanceOperation` or
  `<Schema>Client`: it belongs to the behavior, not a type, and no type's
  config narrows it. A module whose types compose a behavior with
  namespace operations exports, once per behavior, named for the behavior
  (`Comments`, not `<Base>`):
  - `Comments<Op>Params` and `Comments<Op>Result` from each operation's
    schemas (`CommentsInboxParams`, `CommentsInboxResult`,
    `CommentsClaimDeliveriesParams`, ...), and named item types
    `CommentsInboxItem`, `CommentsDelivery` (a claim's item) and
    `CommentsSettledDelivery`, documented from the declaration.
  - `CommentsNamespaceOperations`, `{ inbox: { scope: "namespace";
    params: CommentsInboxParams; result: CommentsInboxResult }; ... }`,
    as `<Base>Operations` maps a schema's.
  - `CommentsNamespaceClient`, constructed from the engine client and a
    namespace, with one method per namespace operation:
    `inbox(params?, options?)`, `claimDeliveries(params?, options?)`,
    `settleDeliveries(params, options?)` (optional params when the schema
    requires none, as `<Schema>Client` decides), each calling the engine
    client's namespace operation call (`POST
    /namespaces/{namespace}/behaviors/Comments/operations/{operation}`,
    whatever the namespace-operations work names the method) and resolving
    to the typed result.
  - `isCommentsNamespaceVeto(error, code?)`, a guard on the behavior and,
    when given, a code of the behavior's vetoes, as `is<Base>Veto` checks a
    schema's.
- Go and Rust types render behavior fields only: they gain `unread` and
  nothing for namespace operations. A typed Go or Rust client of them waits
  for the notifier and the agent dispatcher as stack workers ("Left").
- The MCP tool per namespace operation is the engine's; its description is
  the operation's.

### Batch 3's tests

What `core_comments.rs` (or a `core_comments_inbox.rs` beside it) and the
TypeScript suite should hold, on both databases:

- Markers count stands: a reply begun before a message read and finished
  after it is unread; `markRead` never moves back; `upTo` past every
  message marks what stands; `readUpTo` and `unread` agree with the field.
- `unread` is 0 with no marker, counts others' standing top-level
  messages only, and differs by reader.
- Follows: posting, finishing and being mentioned follow; `role:` does not;
  `begin` does not; `follow({ on: false })` sticks until the next post or
  mention.
- Deliveries: one row per subject with both reasons; never the author; an
  interrupted reply reaches followers; an edit adding a mention returns a
  sent row to pending, one removing it deletes a pending row with no other
  reason.
- Claims: due order; `subjects` and `prefix`; drops `message_gone`, `read`,
  `claim_lapsed`; a second claim takes nothing the first holds; a lapsed
  claim is taken again with `attempt` one higher; schemas the claimer may
  not read, or whose `deliverPermission` it lacks, are never claimed;
  `not_configured` and `forbidden`.
- Settles: each refusal; backoff and the drop at `maxAttempts`; a lapsed
  claim still holding its token settles.
- The inbox: each kind, the order across kinds at one `postedAt`, the
  cursor, `unread` both ways, an unreadable schema's rows never listed,
  and the excerpt.
- `prune`, the instance's delete, `deleteMessage` and `purge` of messages
  with deliveries in each state.

## Batch 4 and 5: acting-for, search, code and attachments

Batch 4 is D77's step 4 on Comments' side: what a message, an edit, a
reaction and a resolve record of a principal acting for a subject (D77
primitive 4), and what that subject's follows, deliveries and unread
counts do. Batch 5 is step 5: `searchMessages`, the `code` block with
`moveAnchor` and `anchorPermission`, the `attachment` block with the
config's `attachments`, and `listPinned`, D77's "Left" item on pinned
messages. Each amends batches 1 to 3 where it says so, and the amended
rule holds. The engine's side of acting-for (the principal's
`actingFor`, the policy's refusal without `actFor`, events' and
Provenance's `for`) is the acting-for work's; the one engine change
batch 5 needs, a full-text match joined to `sql.readable()`, is "The
engine side" below.

The two batches are separable: batch 4 is migration 5 and batch 5
migration 6, so either can ship first, as D77's "each step adds the
storage it needs as a migration of its own" asks. Where a rule of batch
5 reads `for` (an item's member, `movedFor`), it reads the column batch 4
adds; a batch 5 built before batch 4 leaves those members out.

### Terms

- *The caller* is still the call's principal, and *its subject*
  `principal.subject`. Everything batches 1 to 3 key by the caller's
  subject stays keyed by it: `author`, the `clientId`s it may retry, its
  read markers, its follows, its reactions and `mine`, its composing
  messages and `maxDrafts`, the `composing` field, `unread`, `readUpTo`,
  the inbox's caller, `edited_by`, `resolved_by` and `claimed_by`.
- *The caller's for*, F, is the `subject` of `cx.principal().acting_for`
  (Rust `Principal.acting_for: Option<ActingFor { subject }>`, JSON
  `actingFor`) when it is `Some` and differs from the caller's subject;
  otherwise the caller has none. Comments asks the policy nothing about
  it: the engine's policy saw it, and `permissionPolicy` refuses it to a
  principal without `actFor`. A runner principal never acts for
  anyone, and a service that brings an end user cannot also act for
  someone (the primitive's rules), so the schedule's `interrupt` and a
  D37 end-user call have none.
- *The message's for* is the `for_subject` its `post` or `begin` recorded:
  the caller's for at that call, or NULL.
- A block's *index* is its place in the message's `blocks`, from 0.
- *Lengths* are in code points, as batch 2's.

### Batch 4: acting for a subject

#### Decisions

| Question | Decision | Why |
|----------|----------|-----|
| Is the person the author for author-only checks? | No, either way. Every check of batch 1 to 3 that compares a message's `author` with the caller compares it with `principal.subject` alone, and every permission is `cx.can()`'s, which answers for the principal's own (D77 primitive 4: "It adds no permissions"). So an agent acting for Alice may not edit, delete, finish, append to or resolve what Alice wrote (`not_author`), nor interrupt her composing message before it is idle (`not_idle`); and Alice may not edit or delete what an agent wrote for her, whose author is the agent. An agent acting for a moderator does not moderate | The safe default: acting for someone is an attribution, which the policy has already allowed or refused, never a grant. Letting the subject act as author would let any principal the authenticator lets name Alice rewrite Alice's words; letting Alice rewrite the agent's would put her words under the agent's name. A deployment that wants a person to retract what an agent wrote for them gives them `moderatePermission` |
| Whose state does a call write? | The caller's subject's, never the for's: `markRead` and `follow` move the agent's marker and follow; `react` is the agent's reaction (`mine` for the agent); `begin` makes the agent the author and counts against the agent's `maxDrafts` | A marker moved by an agent would mark read for Alice what Alice never saw. A reaction keyed by Alice would let an agent that acts for her take her one reaction of an emoji |
| What is recorded? | The message's for, at `post` and `begin`, on the message; the caller's for on each edit (`comment_edits.for_subject`), reaction (`comment_reactions.for_subject`) and resolve (`resolved_for`). `finish`, `interrupt`, `edit` and `moveAnchor` never change the message's for | The record says what D77 asks, "written by the reviewer agent, for the person who asked it", and each later act says who did it for whom, as its event's `for` does until retention |
| Does the for follow the thread? | Yes, as a mentioned subject follows ("Who follows", the mention's row and its W), when the message comes to stand. Its conversation marker and thread marker do not move to the message | Replies to a message written for Alice answer Alice. She did not write it, so it is not read for her |
| Is the for left out of the deliveries? | Yes, as the author is: recipients are (Mentioned union Followers) minus the author and minus the message's for. An edit that adds a mention leaves the message's for out too | A notifier would tell Alice of words she asked an agent to write; a dispatcher would hand an agent's own errand back to it. Replies reach her through the follow |
| Does the inbox show a message written for the caller as the caller's own? | No: it is another's. `author <> ?` compares the author alone, so it counts in `unread`, in the caller's thread and conversation items and in `markRead`'s result, until the caller reads it | Alice should see what was said in her name. Since she is left out of its deliveries, the inbox and `unread` are where she does |

#### The record and the items

| Member | Batch 4 |
|--------|---------|
| `for` | new, string, after `author` and before `via`: the message's for; absent when NULL |
| `resolved` | `{ by, for?, at }`: `for` the resolver's for, from `resolved_for`, absent when NULL |

The record's members are now: `id`, `thread`, `quotes`, `author`, `for`,
`via`, `state`, `body`, `blocks`, `mentions`, `reactions`, `createdAt`,
`postedAt`, `stand`, `editedAt`, `edits`, `pinned`, `replies`,
`lastReplyAt`, `participants`, `resolved`. `participants` stay authors.

| Item | Batch 4 |
|------|---------|
| `listEdits` | `{ edit, body, blocks, mentions, by, for?, at }`: `for` the editor's for at that edit |
| `listReactions` | `{ subject, for?, at }`: the reactor's for |
| `inbox` | `{ kind, schema, instanceId, message, thread?, author, for?, postedAt, excerpt, unread }`: the item's message's for |
| `claimDeliveries` | `for?` after `author`: the message's for, so a notifier can say "the reviewer agent, for Alice" |

#### Operations, amended

| Operation | Batch 4 |
|-----------|---------|
| `post`, `begin` | write `for_subject` = the caller's for, or NULL. A retry (refusal 1) returns the message whatever the retry's for |
| `post`, `finish`, `interrupt` to `interrupted` | "Deliveries" step 3: recipients = (Mentioned union Followers) minus the author and minus the message's for. "Who follows": after the author's row, the message's for, when it is not NULL and does not start with `role:`, gets the mention's row write on the message's thread with the mention's W (stand for a top-level message, stand - 1 for a reply); no conversation marker of the for moves. A for that is also mentioned gets that write once |
| `edit` | the `comment_edits` row's `for_subject` = the caller's for. With `mentions`: a subject added that is the author or the message's for gets no delivery and no follow |
| `react` | the row's `for_subject` = the caller's for. `unreact` deletes the caller's row whatever its for |
| `resolve` | `resolved_for` = the caller's for; `reopen` sets it NULL with `resolved_by` |
| `deleteMessage` | keeps `for_subject`, as it keeps `author` |
| everything else | unchanged: checks and state are the caller's subject's |

No refusal is new, and none changes: `not_author`'s and `not_idle`'s
conditions read the caller's subject, as before.

A logged event's `for` is the call's (the engine stamps it); the record's
`for` is the message's. They differ when an agent acting for Bob edits a
message it wrote for Alice: the event says `for: "bob"`, the record
`for: "alice"`, and `listEdits` keeps Bob as that edit's `for`.

#### Migration 5, `acting_for`

`BehaviorMigration::new(5, "acting_for")`, the same on both dialects, four
`ALTER TABLE ... ADD COLUMN`, each nullable, existing rows NULL:

| Table | Column | Spec | Holds |
|-------|--------|------|-------|
| `comment_messages` | `for_subject` | text | the message's for |
| `comment_messages` | `resolved_for` | text | the resolver's for, while `resolved_by` is set |
| `comment_edits` | `for_subject` | text | the editor's for |
| `comment_reactions` | `for_subject` | text | the reactor's for |

No index: nothing filters by them. `MESSAGE_COLUMNS` gains `for_subject`
and `resolved_for`. `for` is reserved in both dialects, hence the names.

#### Batch 4's tests

- A post and a begin by a principal acting for `alice` record `for`, a
  `finish` and the schedule's `interrupt` keep it, and the event's `for`
  agrees; a principal acting for itself records none.
- The agent acting for Alice may not edit, delete, finish or resolve
  Alice's message (`not_author`) or interrupt her draft before it is
  idle (`not_idle`); Alice may not edit the agent's message written for
  her; the agent may edit its own message written for Alice, and the
  edit's `for` is its call's.
- Alice follows the thread of a message written for her, gets no
  delivery of it even when mentioned or following, gets the delivery of
  the next reply, and its `unread` and thread item count the message
  until she reads it; the agent's marker moves, hers does not.
- `react` acting for Alice: `mine` for the agent, not for Alice; a
  second `react` with the same emoji acting for Bob is `already_reacted`.
- `markRead` acting for Alice moves the agent's marker.
- `resolved.for`, `listEdits`' and `listReactions`' `for`, the inbox's and
  the claim's `for`.
- Migration 5 on a database with batch 3's rows: every `for` absent.

### Batch 5: config

| Member | Type | When absent | Meaning |
|--------|------|-------------|---------|
| `blocks` | unique array of `"ref"`, `"action"`, `"code"`, `"attachment"` | every kind | as batch 1's, with two kinds more |
| `anchorPermission` | string, `minLength` 1 | only the author and a moderator move an anchor | the permission that lets a caller `moveAnchor` a code block on another's message |
| `attachments` | object, below | `{}` | what an attachment block may describe |

`attachments`, every member optional, no others:

| Member | Type | When absent | Meaning |
|--------|------|-------------|---------|
| `maxBytes` | integer 1..9007199254740991 | no limit | the largest `bytes` an attachment block may give |
| `mediaTypes` | unique array, at least one, of `type/subtype` or `type/*`, lower case, pattern `^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/([a-z0-9][a-z0-9!#$&^_.+-]{0,126}\|\*)$` | any | the media types an attachment may have |
| `schemes` | unique array, at least one, of URI schemes, lower case, pattern `^[a-z][a-z0-9+.-]{0,31}$`, none of `javascript`, `vbscript`, `data`, `file`, `blob`, `about` | `["https"]` | the schemes an attachment's `uri` may use. The list is the whole list: `["bucket"]` refuses `https` |

`CommentsConfig` gains `anchor_permission: Option<String>` and
`attachments: AttachmentsConfig { max_bytes: Option<i64>, media_types:
Option<Vec<String>>, schemes: Vec<String> }`, `schemes` `["https"]` when
absent. A config change applies to the next call, as batch 1's: a
narrowed `attachments` leaves posted blocks as they are.

### Batch 5: blocks

Two kinds join batch 1's table, each a member of the `oneOf` of `post`'s,
`finish`'s and `edit`'s `blocks` with `additionalProperties: false`:

| Kind | Members (paramsSchema) | The behavior's own checks (`invalid_argument`) | The config's checks (veto) |
|------|------------------------|-----------------------------------------------|----------------------------|
| `code` | `repo` 1..512, no space or control character (`^[^\x00-\x20\x7F]+$`); `path` 1..1024, no control character; `revision` 1..256, as `repo`; `lines` exactly two integers 1..2147483647; `snippet` 0..8000 code points | `/blocks/<i>/lines` `must have its first line at most its last` when `lines[0] > lines[1]`; then `/blocks/<i>/path` `must be a relative path: no leading or trailing /, no \, and no empty, . or .. part` when the path starts or ends with `/`, holds `\`, or split at `/` has a part that is `""`, `"."` or `".."` | none |
| `attachment` | `uri` 3..2048, `^[a-z][a-z0-9+.-]{0,31}:[^\x00-\x20\x7F]+$` (a lower-case scheme, a colon, no space or control character); `name` 1..255, no `/`, `\` or control character, not white space alone; `mediaType` `type/subtype` in lower case, no parameters, at most 255; `bytes` integer 0..9007199254740991; `sha256` `^[0-9a-f]{64}$` | `/blocks/<i>/uri` `must be an https URL with a host` when its scheme is `https` and it does not start with `https://` followed by a character that is not `/`, `?` or `#` | `attachment_not_allowed`, below |

The scheme is the `uri`'s text before its first `:`. The config's checks
of one attachment block, in order, each the veto `attachment_not_allowed`
with details `{ block, member, value, allowed }`:

| # | When | `member` | `value` | `allowed` |
|---|------|----------|---------|-----------|
| 1 | the scheme is not in `schemes` (`["https"]` when absent) | `"uri"` | the scheme | the schemes |
| 2 | `mediaTypes` is given and the block's `mediaType` equals none of them and matches no `type/*` of them (the part before `/` equal) | `"mediaType"` | the media type | the media types |
| 3 | `maxBytes` is given and `bytes > maxBytes` | `"bytes"` | `bytes` | `maxBytes` |

The engine never fetches the `uri`, checks no `sha256` against anything,
and asks nothing of a bucket: the deployment's upload route stored the
file and decides who downloads it. A client links the `uri` only by a
scheme it knows.

A code block's `snippet` is the text as the poster captured it. A block's
JSON is stored as given (`preserve_order`), so its members come back in
the order given, plus, once moved, `original`, `moves`, `movedAt`,
`movedBy` and `movedFor`, in that order ("moveAnchor").

On `edit`, as for ref blocks: an attachment block equal, as a JSON value,
to an attachment block of the message before the edit is not held to the
config again, so an edit that moves an action's status does not fail on
a file whose type the config has since dropped. A code block given at
index `i` whose `repo`, `path`, `revision`, `lines` and `snippet` equal
those of a code block at index `i` before the edit is stored as the block
before, its move members kept; any other code block an edit gives is new,
with none. A client that edits a message gives back the code blocks as it
read them, the move members dropped by the paramsSchema, and keeps their
history.

The order of every operation that gives blocks becomes:

| # | `post` | When |
|---|--------|------|
| 1 | (retry) | |
| 2 | `invalid_argument` `/body` | |
| 3 | `invalid_argument` `/quotes` `needs thread` | |
| 4 | `invalid_argument` at `/blocks/<i>/...` | the first block, by index, that fails its kind's own checks: an action's `data` (batch 1), a code block's `lines` then `path`, an attachment's `uri` |
| 5 to 11 | `no_message`, `not_a_thread`, `composing`, `no_message`, `not_in_thread`, `deleted`, `composing` | batch 2's 5 to 11 |
| 12 | `block_not_allowed` | the first block whose kind the config does not list |
| 13 | `attachment_not_allowed` | the first attachment block, by index, the config's checks refuse, its first failing check |
| 14 | `ref_not_found` | |

`finish`: its 2 (`/blocks/<i>/data`) becomes row 4 above; `block_not_allowed`,
then `attachment_not_allowed`, then `ref_not_found`. `edit`: its 3 becomes
row 4 above; after `block_not_allowed`, `attachment_not_allowed` for an
attachment new to the message, then `ref_not_found` for a ref new to it.

### Batch 5: `moveAnchor({ message, block, revision, lines })`

`"writes": true, "beside": true`, logged. Moves a code block to the same
code at another revision, where a caller that knows the repository found
it: a git extension after a push, say, which the engine is not. Result
and event `result`: the record after.

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` `/lines` `must have its first line at most its last` | `lines[0] > lines[1]` |
| 2 | `no_message` | |
| 3 | `deleted` `{ message }` | |
| 4 | `composing` `{ message }` | |
| 5 | `not_author` `{ message, author }` | the caller's subject is not the author, the config names no `anchorPermission` or the caller lacks it, and the caller does not moderate. Ask `cx.can()` only when the subject is not the author: `anchorPermission` first, then `moderatePermission` |
| 6 | `no_block` `{ message, block, blocks }` | `block` is not below the message's block count, `blocks` |
| 7 | `not_code` `{ message, block, kind }` | the block at `block` is not a code block; `kind` its kind |
| 8 | `anchor_unchanged` `{ message, block, revision, lines }` | the block's `revision` and `lines` already equal the given ones; the details give them |

An interrupted message is moved as a posted one. A move to the same
revision with other lines is allowed: it corrects a misplaced anchor.

Writes: the message's `blocks` column alone, with the block at `block`
replaced by: its members as they were, but `revision` and `lines` the
given ones; `original` = `{ revision, lines }` as they were, when the
block had no `original` (its first move), else kept; `moves` = its
`moves` + 1, 1 at the first; `movedAt = now`; `movedBy` = the caller's
subject; `movedFor` = the caller's for, removed when it has none. The five
move members are written in that order after the given ones. Nothing
else: `edited_at`, `edits`, `comment_edits`, the counts, the stand, the
search index, the deliveries and the follows stay.

Why it is not an edit: it changes nothing its author wrote. The anchor
follows code the engine cannot see move, and a bot moves it on each push.
As an edit it would list the bot in `listEdits` with a copy of the text
each time, spend the 500 edits a message has, be refused once
`editWindowMs` passed though the comment's words are unchanged, mark the
message edited to every reader, and need `moderatePermission` for a bot
that should hold only `anchorPermission`. The history lives in the log:
the `post` (or `edit`) event holds the anchor given, each `moveAnchor`
event its params and the record after, and the record keeps the first
anchor and the last move after retention prunes the events.

A moved block, as the record returns it:

```json
{ "kind": "code", "repo": "github.com/acme/shop", "path": "src/orders.rs", "revision": "e41b07c", "lines": [44, 52],
  "snippet": "fn place(...) {", "original": { "revision": "9c1f2e7", "lines": [40, 48] },
  "moves": 2, "movedAt": 1793000000000, "movedBy": "service:git-anchors" }
```

### Batch 5: `listPinned({ limit?, cursor? })`

Read-only, on the instance. Every pinned message of the conversation,
top-level and replies, by id ascending, paged as `listEdits` over the
message id (`page_request`, `limit` 1..100, 50 when absent; a cursor it
did not write is `invalid_argument` at `/cursor`). Result `{ items,
next }`, each item the record. Only a standing message is pinned: `pin`
refuses a composing one and `deleteMessage` unpins. No refusal but the
cursor's.

```sql
SELECT <MESSAGE_COLUMNS> FROM <comment_messages>
 WHERE namespace = ? AND schema = ? AND id = ? AND pinned = 1 AND message > ?
 ORDER BY message LIMIT ?                                   -- limit + 1
```

through `comment_messages_pinned` ("Migration 6"). The order is the
conversation's, not the order they were pinned in: no pin time is kept
("Left for later batches"). A `listMessages` filter was not taken: its listing is one
level (top-level, or one thread's replies) with `around`, `root` and
`readUpTo` of that level, and pinned messages span both.

### Batch 5: `searchMessages({ query, schema?, instanceId?, thread?, limit?, cursor? })`

`"scope": "namespace"`, read-only, no event. The policy is asked `read`
with `{ behavior: "Comments", operation: "searchMessages" }`, so
`permissionPolicy` lets it through, and only rows of `sql.readable()`'s
schemas leave its statement. Result `{ items, next }`.

`sql.readable()` keeps the schemas the policy lets the caller `read` by
the schema-wide question, which names no operation; it asks nothing
about `listMessages` or `getMessage`. A policy that refuses those on a
schema by a rule that names the operation still lets `searchMessages`
return that schema's messages and snippets, as it lets `inbox` list
them (batch 3). A deployment that would hide a schema's messages from
both refuses `read` on the schema, or refuses the namespace operations
through the behavior question (`{ "behavior": "Comments", "operation":
["searchMessages", "inbox"], "allow": false }`).

An item, members in this order:

| Member | Type | Value |
|--------|------|-------|
| `schema`, `instanceId`, `message` | string, string, integer | the message found |
| `thread` | integer | its thread, when it is a reply |
| `author`, `for`, `via` | string | as the record's (`for` and `via` when not NULL) |
| `state` | `posted` or `interrupted` | |
| `postedAt` | integer | |
| `editedAt` | integer | when it was edited |
| `rank` | integer | its place in the search's order, from 1, across pages |
| `snippet` | array of `{ text, match }` | a few words of the body around its matches, as Search's: at most 12 words, `...` where it cuts, each part saying whether it is a match |

What is searched: the `body` of every *standing* message (`posted`,
`interrupted`). A composing message's draft is never indexed, a deleted
message's row leaves the index at its delete, a purged one's at its
purge, and an edit replaces the indexed body, so the text before an edit
is never found. Blocks are not searched.

The query. D62's `SearchQuery` filters with `LIKE` and does not rank, so
it does not fit an FTS index; Search's plain query (D16, "Search") does,
and `searchMessages` takes it, plain words alone, with D62's count of
words:

1. `query` is 1..500 code points with a non-space (paramsSchema).
2. NUL becomes a space; the text splits at JavaScript white space
   (Search's `is_js_space`); empty words drop; the first 8 words count.
3. Each word becomes an FTS5 string, `"` doubled and the word quoted, and
   the words are joined by a space: implicit AND. So quotes, `AND`, `OR`,
   `NOT`, `NEAR`, `*`, `:` and parentheses are text, and the expression
   is never refused. A word FTS5 splits (`slips-a-week`) is a phrase of
   its parts; a word that tokenizes to nothing counts for nothing, and a
   query of only such words finds nothing, without an error. No
   `syntax: "fts5"`: a caller's prefix (`a*`) reads every term it starts,
   which a namespace-wide search should not offer every reader.

| # | Refusal | When |
|---|---------|------|
| 1 | `invalid_argument` `/instanceId` `needs schema` | `instanceId` without `schema` |
| 2 | `invalid_argument` `/thread` `needs instanceId` | `thread` without `instanceId` |
| 3 | `invalid_argument` `/cursor` `is not a cursor this operation returned` | `page_request`'s |

A `schema` the caller may not read, or that does not compose Comments, an
`instanceId` with no messages and a `thread` that names nothing find
nothing: they are filters, never refused, so a search says nothing of
what the caller may not read.

Paging. `page_request`: `limit` 1..100, 50 when absent; the cursor is
`after:<rank>`, an offset into the ranking, as Search's. A search reaches
its first 1000 hits: with `after >= 1000` the page is empty and `next`
null; otherwise it reads `min(limit + 1, 1000 - after)` hits from offset
`after`, and `page(hits, limit, rank)` gives the items and `next`. A
write between two pages can move a message across the boundary, as in
Search.

The match is one `sql.fulltext` call:

| `FullTextQuery` member | Value |
|------------------------|-------|
| `index`, `columns`, `weights` | `"comment_search_text"`, `["body"]`, `[1.0]` |
| `snippets` | `{ columns: 1, open: "\u{2}", close: "\u{3}", ellipsis: "...", words: 12 }`, Search's |
| `keys`, `key`, `select` | `"comment_search"`, `"row"`, `["schema", "id", "message"]` |
| `filters` | `("namespace", the call's)`, then `("schema", schema)`, `("id", instanceId)`, `("thread_root", thread)` for each given |
| `readable` | `Some("schema")` (new, "The engine side") |
| `exclude` | `None` |
| `expression`, `caller` | the words above, `false`: a refusal is a defect, as Search's plain query |
| `limit`, `offset` | the count above, `after` |

Then one statement reads the hits' messages:

```sql
SELECT schema, id, message, thread, author, for_subject, via, state, posted_at, edited_at
  FROM <comment_messages>
 WHERE namespace = ? AND state IN ('posted', 'interrupted')
   AND ((schema = ? AND id = ? AND message = ?) OR ...)         -- one group per hit
```

Items are in the hits' order, `rank = after + position + 1` over the hits
read; `page` runs on the hits, then a hit whose message the second
statement did not find standing (a write committed between the two
statements, on Postgres) is dropped from the items, and no rank moves.
The snippet's parts split the hit's snippet at `\u{2}` (a match starts)
and `\u{3}` (it ends), as Search's `snippet_of`; share that helper.

Ranking per dialect, as the engine's full-text index ranks (D58): on
SQLite, FTS5's `bm25` over `body`, then `row` ascending, the order the
messages were first indexed; on Postgres, `ts_rank_cd` over `body_tsv`
labelled `A`, then `row`. The two databases find the same messages but
for the tokens Postgres's parser keeps whole (an email address, a URL, a
path, a number with a point), and order them differently. The index is
one per engine, shared by every namespace and schema, so bm25's
statistics are taken over all of them: the hits are the caller's, their
order can move with another namespace's text, and no score is returned,
as Search returns none.

SQLite, as `sql.fulltext` writes it with the readable join:

```sql
WITH <readable> AS (SELECT CAST('Ticket' AS TEXT) AS schema UNION ALL ...)
SELECT k.schema AS schema, k.id AS id, k.message AS message, k.row AS row,
       snippet(<comment_search_text>, 0, ?, ?, ?, 12) AS s0
  FROM <comment_search_text> CROSS JOIN <comment_search> AS k ON k.row = <comment_search_text>.rowid
  JOIN <readable> AS r ON r.schema = k.schema
 WHERE <comment_search_text> MATCH ? AND k.namespace = ? [AND k.schema = ?] [AND k.id = ?] [AND k.thread_root = ?]
   AND k.row IS NOT ?
 ORDER BY bm25(<comment_search_text>, ?), k.row
 LIMIT ? OFFSET ?
```

Postgres:

```sql
WITH <readable> AS (...)
SELECT m.schema, m.id, m.message, m.row,
       ts_headline(CAST('<prefix>comment_search_text_config' AS regconfig), m.c0, m.q, ?) AS h0, m.c0 AS t0
  FROM (
    SELECT k.schema AS schema, k.id AS id, k.message AS message, k.row AS row, t.body AS c0, q.q AS q,
           ts_rank_cd(CAST('{0, 0, 0, 1}' AS real[]), setweight(t.body_tsv, 'A'), q.q) AS rank
      FROM <comment_search_text> AS t JOIN <comment_search> AS k ON k.row = t.rowid
      JOIN <readable> AS r ON r.schema = k.schema
     CROSS JOIN to_tsquery(CAST('<prefix>comment_search_text_config' AS regconfig), ?) AS q(q)
     WHERE t.all_tsv @@ q.q AND k.namespace = ? [AND k.schema = ?] [AND k.id = ?] [AND k.thread_root = ?]
       AND k.row IS DISTINCT FROM ?
     ORDER BY rank DESC, k.row
     LIMIT ? OFFSET ?
  ) AS m
 ORDER BY m.rank DESC, m.row
```

#### The engine side

`sql.fulltext` filters `keys` by equality alone, so it cannot keep a
namespace operation's hits to `sql.readable()`'s schemas. Batch 5 needs
one addition to `superschematic_engine_storage::fulltext`:

- `FullTextQuery` gains `readable: Option<&'a str>`: a column of `keys`
  holding a schema name. When `Some`, both match statements join
  `<readable> AS r ON r.schema = k.<column>` (SQLite after the `CROSS
  JOIN`, Postgres in the inner `FROM`), the name from
  `BehaviorSql::readable()`, which refuses outside a namespace operation
  (a `BehaviorError`); `prepare` then puts the relation's definition
  ahead of the statement, as for any statement that names it. When the
  principal may read no schema, the relation has no row and the match
  none.
- Search, and every caller today, passes `None`; nothing else changes.
- `create_fulltext_with(name, columns, Unaccent)` beside
  `create_fulltext`, which is `Unaccent::Required`: with
  `Unaccent::IfAvailable` a Postgres where `unaccent` cannot be had (the
  database lacks it and the engine may not create it, or the role may not
  use it) gets a configuration of `simple` alone, and the migration goes
  on ("Migration 6").

The alternatives were worse: a `fulltext` call per readable schema, whose
hits cannot be merged by score since a hit carries none (Search's
every-schema search merges by reciprocal rank for that reason, though
Comments has one index and one weight across schemas); or filtering in
the handler, which lets rows of unreadable schemas leave a statement and
breaks the offset paging.

#### Keeping the index

The index is two tables ("Migration 6"): `comment_search`, one row per
standing message, which gives it a `row`, and the full-text index
`comment_search_text`, whose `rowid` is that `row` and whose one column
is the body. Each write is plain SQL, as Search writes its own:

| Call | Writes |
|------|--------|
| `post`, `finish`, `interrupt` to `interrupted` (a message comes to stand) | `INSERT INTO <comment_search> (namespace, schema, id, message, thread_root) VALUES (?, ?, ?, ?, ?)` with `thread_root` = `thread`, or the message's id when top-level; read its `row` back (`SELECT row ... WHERE namespace = ? AND schema = ? AND id = ? AND message = ?`); `INSERT INTO <comment_search_text> (rowid, body) VALUES (?, ?)` |
| `edit` with `body`, of a standing message | `DELETE FROM <comment_search_text> WHERE rowid = ?` then the insert with the same `row`, as Search's `index` replaces a row. An edit without `body` writes nothing |
| `deleteMessage` of a standing message | `DELETE FROM <comment_search_text> WHERE rowid = ?`, then the `comment_search` row. A composing message has none |
| `purge` | the same for every purged message that has a row: `... WHERE rowid IN (SELECT row FROM <comment_search> WHERE namespace = ? AND schema = ? AND id = ? AND message IN (...))`, then the keys |
| the instance's delete (`afterChange` on `Delete`) | the same for the instance: `... WHERE rowid IN (SELECT row FROM <comment_search> WHERE namespace = ? AND schema = ? AND id = ?)`, then its keys |
| `interrupt` to `deleted`, `begin`, `appendDraft`, `moveAnchor`, reactions, pins, resolves, `markRead`, `follow` | nothing |

Each write is in the call's transaction, so a call that fails takes its
index change back, and a search never finds a message the tables do not
hold standing (but the race the second statement covers).

### Batch 5: migration 6, `search`

`BehaviorMigration::new(6, "search")`:

- `.table(TableSpec::new("comment_search").row_id("row")`, columns
  `namespace`, `schema`, `id` text not null, `message` integer not null,
  `thread_root` integer not null, `.unique(&["namespace", "schema", "id",
  "message"])`): the keys. The unique index serves a message's lookups
  and an instance's delete by its prefix.
- `.up(|sql| ...)`:
  1. `sql.create_fulltext_with("comment_search_text", &["body"],
     Unaccent::IfAvailable)`: on SQLite the FTS5 table (`unicode61
     remove_diacritics 2`); on Postgres the table `rowid`, `body`,
     `body_tsv`, `all_tsv`, its GIN index and its text search
     configuration, with `unaccent` where the database has it or the
     engine may create it, and `simple` alone where not (case folds,
     diacritics stay), so the migration never fails for the extension.
     Search's `create_fulltext` needs it (`Unaccent::Required`), but every
     engine that composes Comments runs this migration, Search or not, and
     one whose role may not create `unaccent` must still migrate.
  2. The backfill, every standing message in stand order per instance:

     ```sql
     INSERT INTO <comment_search> (namespace, schema, id, message, thread_root)
     SELECT namespace, schema, id, message, COALESCE(thread, message) FROM <comment_messages>
      WHERE state IN ('posted', 'interrupted')
      ORDER BY namespace, schema, id, stand;
     INSERT INTO <comment_search_text> (rowid, body)
     SELECT s.row, m.body FROM <comment_search> s
       JOIN <comment_messages> m
         ON m.namespace = s.namespace AND m.schema = s.schema AND m.id = s.id AND m.message = s.message;
     ```

  3. Index `comment_messages_pinned` on `comment_messages (namespace,
     schema, id, message)` where `pinned = 1`, named through
     `sql.table(name)` as batch 2's; `listPinned`'s statement says
     `pinned = 1` itself.

Code and attachment blocks and the moves need no storage: they are the
`blocks` column's JSON.

Deleting the instance also removes its `comment_search` and
`comment_search_text` rows ("Keeping the index").

### Batch 5: operations, amended

| Operation | Batch 5 |
|-----------|---------|
| `post`, `finish` | the blocks' checks ("Batch 5: blocks"); the index insert when it stands |
| `interrupt` | to `interrupted`: the index insert. Its blocks are `[]`, so nothing else |
| `edit` | the blocks' checks, an attachment new to the message held to the config, a code block unchanged at its index keeping its moves; the index replaced when `body` is given. Refusals as "Batch 5: blocks" orders them |
| `deleteMessage` | deletes the index rows; the blocks clear as before, moves with them |
| `purge` | deletes the purged messages' index rows |
| `pin` | unchanged; `listPinned` reads what it sets |
| `listEdits` | an item's `blocks` are as they were before the edit, a code block's move members included |
| `listMessages`, `getMessage` and every record | blocks of the four kinds, code ones with their move members |

### Batch 5: refusals

New veto codes:

| Veto code | details |
|-----------|---------|
| `attachment_not_allowed` | `{ block, member, value, allowed }` |
| `no_block` | `{ message, block, blocks }` |
| `not_code` | `{ message, block, kind }` |
| `anchor_unchanged` | `{ message, block, revision, lines }` |

`deleted`, `composing` and `not_author` also cover `moveAnchor`, with
their details. `block_not_allowed` covers the two new kinds.

### What the generators expose

For `behaviortypes` (typegen, rustgen) and `engineclientgen`:

- **The block union.** `<Base>CommentBlock` is post's `oneOf`, so it
  gains `code` and `attachment` from the declaration with no code; the
  `config.blocks` narrowing (`blockKind` over each kind's `const`) narrows
  `post`'s, `finish`'s and `edit`'s blocks to any subset of the four as it
  does of two. Tests and goldens gain the kinds.
- **Posted blocks.** A record's code block may hold the move members,
  which the paramsSchema's closed code block does not declare. Add
  `<Base>CommentPostedBlock`, "A block of a message of <Base>'s Comments
  as the record holds it: a code block that moveAnchor moved also holds
  its first anchor and its last move": the `oneOf` of `<Base>CommentBlock`
  with the code member extended by optional `original: { revision:
  string; lines: number[] }`, `moves: number`, `movedAt: number`,
  `movedBy: string`, `movedFor: string`, typed in `narrowComments` as
  `threads` is. `<Base>CommentMessage`'s `blocks` and `listEdits`' items'
  `blocks` are `<Base>CommentPostedBlock[]`; what a call gives stays
  `<Base>CommentBlock`.
- **The record.** `for?: string` after `author`, and `resolved.for?`,
  come from the declaration with no code. In TypeScript `for` is a valid
  member name; a Rust type that renders the record names it `r#for` or
  `for_subject` with `#[serde(rename = "for")]`, a Go one `For` with
  `json:"for,omitempty"` (neither renders records today: Go and Rust
  render behavior fields only).
- **`moveAnchor`.** Add it to `commentsMessageResults`: it returns
  `<Base>CommentMessage`. Its params are as declared, on `<Schema>Client`
  as every instance operation.
- **`listPinned`.** Its result's `items` are `<Base>CommentMessage`, set
  as `listMessages`' are (`setItems(n.Results["listPinned"], ref(message,
  ""), "properties", "items")`).
- **`searchMessages`.** A namespace operation, so it joins
  `CommentsNamespaceOperations` and `CommentsNamespaceClient` from the
  declaration: `searchMessages(params, options?)`, params required.
  `coreNamespaceItems` gains `"Comments.searchMessages":
  "CommentsSearchHit"`, documented "A message searchMessages found: where
  it is, who wrote it and for whom, its place in the search's order, and
  a few words of its body around the matches, in parts."
- **The items' `for`.** `CommentsInboxItem` and `CommentsDelivery` gain
  `for?` from the declaration; `listEdits`' and `listReactions`' items
  too.
- **Config.** `anchorPermission` and `attachments` are typed from the
  configSchema with no code.
- Go and Rust types render behavior fields only: no field is new, so they
  change only through the declaration they embed. The MCP tool
  `comments.searchMessages` is the engine's.

### Batch 5's tests

What `core_comments.rs` (or a `core_comments_search.rs` beside it) and the
TypeScript suite should hold, on both databases:

- Code blocks: a post with one; `lines` out of order and each bad `path`
  (`/a`, `a/`, `a//b`, `./a`, `a/../b`, `a\b`) refused at their pointer;
  `block_not_allowed` under `blocks: ["ref"]`.
- Attachments: `https://` without a host refused; each of the config's
  three checks in order, with its details; `schemes: ["bucket"]` refusing
  `https`; `image/*` taking `image/png`; an edit that keeps an attachment
  the narrowed config refuses, and one that adds it refused.
- `moveAnchor`: each refusal in order; the author, a holder of
  `anchorPermission`, a moderator; the first move records `original`,
  the second keeps it and counts 2; `editedAt`, `edits`, `listEdits`, the
  counts and the index unchanged; past `editWindowMs` allowed; an edit
  giving the block back keeps its moves, one changing its `snippet` drops
  them; the event's record.
- `listPinned`: replies and top-level messages, id order, paging, a
  deleted message gone from it.
- `searchMessages`: finds posted and interrupted messages, never a
  draft, a deleted or a purged message or the text before an edit; an
  instance's delete leaves nothing; `schema`, `instanceId` and `thread`
  narrow, `thread` holding the top-level message and its replies; refusals
  1 to 3; a schema the caller may not read is never found, nor named; a
  query of `"`, `AND`, `*` and `NEAR` is text; the 9th word does not
  count; paging, `rank` across pages and the 1000-hit end; the snippet's
  parts; the item's `for` and `via`.
- Migration 6 on batch 3's rows: every standing message found, no
  composing or deleted one; `listPinned` on pinned rows of before.
- The engine side: `fulltext` with `readable` outside a namespace
  operation is a `BehaviorError`; with no readable schema it matches
  nothing; Search's matches are unchanged.

## Left for later batches

Not declared and not built in batches 1 to 5:

- Following a whole conversation (an instance's assignee hears of each
  top-level message); a mention of a group in its members' inboxes,
  which needs roles the principal does not carry; an inbox past tens of
  thousands of followed threads, which needs a per-subject table the
  stands keep; a typed Go or Rust client of the namespace operations,
  with the notifier and the agent dispatcher as stack workers.
- Search beyond bodies: blocks' text (a code snippet, an attachment's
  name, an action's title), a recency order, a prefix on the last word as
  a typing search box wants, and vectors over messages (D77's "Left").
- A pin's time and who pinned, for a listing in the order pins were
  made.
- Moving Search's every-schema search onto namespace operations (D77's
  "Left").

Each batch adds its own migration (batch 2 adds 3, batch 3 adds 4, batch
4 adds 5, batch 5 adds 6) rather than the single migration 2 D77
describes, since batch 1 shipped alone.
