// Type-level checks of Comments<T, C> (D77, D103's step 4). Nothing
// imports this file: `bun run typecheck`, which `make ts` runs, checks it,
// and fails on an expected error (@ts-expect-error) that no longer errors.
import type { Comments, CommentsOn } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. A
// ticket's conversation, which a caller holding tickets.moderate
// moderates.
export abstract class Ticket implements Comments<Ticket, { moderatePermission: "tickets.moderate"; editWindowMs: 900000 }> {
  id: string;
  title: string;
}

// A conversation of refs and code blocks alone, its bodies at most 2000
// code points.
export abstract class Review implements Comments<Review, { blocks: ["ref", "code"]; maxBodyLength: 2000 }> {
  id: string;
  title: string;
}

const plain: CommentsOn = {};
const permissions: CommentsOn = { pinPermission: "reviews.pin", resolvePermission: "reviews.resolve" };

// An attachment block waits for attachments on the core, and a config's
// streamed replies and deliveries for signals and namespace operations.
// @ts-expect-error a table's Comments takes no attachment block
const attachments: CommentsOn = { blocks: ["attachment"] };
// @ts-expect-error a table's Comments takes no composeTimeoutMs
const drafts: CommentsOn = { composeTimeoutMs: 60000 };
// @ts-expect-error a table's Comments takes no deliverPermission
const deliveries: CommentsOn = { deliverPermission: "notify" };
