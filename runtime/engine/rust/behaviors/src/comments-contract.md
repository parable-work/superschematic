# Comments v2, batches 1 and 2: the contract

This note is the contract the Rust behavior (`comments.rs`), the Go
generators and the corpora implement for batches 1 and 2 of D77
("Comments, rebuilt", steps 1 and 2 of its build order). The declaration is
`declarations/Comments.behavior.json`, the canonical copy of
`internal/registry/behaviors/comments.behavior.json`; where this note and
the declaration disagree, the declaration's schemas win and this note is
the bug.

Batch 1 is posts, edits, deletes, purge, reactions, pins, threads and
resolve, every writing operation beside the instance (D77 primitive 1,
`"beside": true`, logged). Batch 2, streamed replies over signals (D77
primitive 2), is "Batch 2" below: where it amends a rule of batch 1 it
says so, and the amended rule holds. What both leave out is listed at the
end.

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

## Left for later batches

Not declared and not built in batches 1 and 2, each in D77's order:

- Batch 3 (namespace operations): `markRead`, `follow`, the `unread`
  field, `inbox`, `claimDeliveries`, `settleDeliveries`,
  `deliverPermission`, `maxAttempts`; `comment_reads`, `comment_follows`,
  `comment_deliveries`; deliveries for mentions and followers.
- Batch 4 (acting for a subject): the record's `for`, stored as
  `for_subject`.
- Batch 5: `searchMessages`; `code` and `attachment` blocks and the config
  kinds for them; `moveAnchor` and `anchorPermission`.

Each batch adds its own migration (batch 2 adds 3, and later ones 4,
5, ...) rather than the single migration 2 D77 describes, since batch 1
shipped alone.
