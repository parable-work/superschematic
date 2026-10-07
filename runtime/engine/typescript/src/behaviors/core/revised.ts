/*
A link's target moving on (D16, amended), as Reactions' revised rule and
Lease's directOn hear it. An event of the target's schema moves it on
when it is a new revision of Revisions, an update or an operation whose
change carries revision, or a release of Branches, its releaseCommit,
whose patch carries the release pointer's new version (Branches'
release field); a target whose schema composes neither gains neither,
which the rule that names the link takes for a config error. The
instances it moves on are the ones of the rule's own schema whose link
points at the target, found with Links' listLinked: where the link pins
what the event made, a revision or a release, only the ones the target
has moved past (stale), so an instance linked to the new revision or
release since is left alone; any other link moves every one on.
*/

import { BehaviorError } from '../../errors.js';
import type { EngineEvent, OperationChange } from '../../events/log.js';
import type { BehaviorScope, FrozenJSON } from '../behavior.js';
import type { LinkPin } from './links.js';

/**
 * How an event moved a link's target on: a revision, or a release, by the
 * release pointer's version it made (1 at the first) and the commit it
 * names.
 */
export type TargetMove =
  | { readonly kind: 'revision'; readonly revision: number }
  | { readonly kind: 'release'; readonly release: number; readonly commit: string };

// Links' largest page of listLinked.
const PAGE = 500;

/**
 * targetMove reads how an event of a link's target schema moved its
 * instance on, or undefined when it did not: a create, a delete, and any
 * change that is neither a revision nor a release. A releaseCommit is a
 * release whatever else its patch carries. form names the rule, for the
 * BehaviorError a schema that composes neither Revisions nor Branches
 * gets.
 */
export function targetMove(scope: BehaviorScope<unknown>, behavior: string, form: string, link: string, schema: string, event: EngineEvent): TargetMove | undefined {
  if (event.schema !== schema || event.kind === 'create' || event.kind === 'delete') {
    return undefined;
  }
  const revisions = scope.schemas.config(schema, 'Revisions') !== undefined;
  const branches = scope.schemas.config(schema, 'Branches') !== undefined;
  if (!revisions && !branches) {
    throw new BehaviorError(behavior, `${form} names link ${link} to ${schema}, which composes neither Revisions nor Branches, so it gains no revision or release`);
  }
  const change = event.change as Record<string, unknown> | null;
  const operation = event.kind === 'operation' ? (change as OperationChange | null) : null;
  const patch = operation !== null ? (operation.patch as Record<string, unknown> | undefined) : event.kind === 'update' ? change : undefined;
  if (branches && operation?.behavior === 'Branches' && operation.operation === 'releaseCommit') {
    const release = typeof patch?.release === 'number' ? patch.release : Number(operation.params.version) + 1;
    return { kind: 'release', release, commit: String(operation.params.commit) };
  }
  if (revisions && patch !== undefined && patch !== null && Object.prototype.hasOwnProperty.call(patch, 'revision')) {
    return { kind: 'revision', revision: Number(patch.revision) };
  }
  return undefined;
}

/**
 * linkedTo lists, in Links' order, the instances of the scope's schema
 * whose link points at the target id, through Links' listLinked as the
 * scope's principal: with stale, only the ones whose pin the target has
 * moved past.
 */
export function linkedTo(scope: BehaviorScope<unknown>, link: string, id: string, stale: boolean): string[] {
  const found: string[] = [];
  let cursor: string | undefined;
  do {
    const page = scope.instances.invokeSchema(scope.schema, 'listLinked', {
      name: link,
      id,
      limit: PAGE,
      ...(stale ? { stale: true } : {}),
      ...(cursor === undefined ? {} : { cursor }),
    } as FrozenJSON) as { items: Array<{ id: string }>; next: string | null };
    found.push(...page.items.map((item) => item.id));
    cursor = page.next ?? undefined;
  } while (cursor !== undefined);
  return found;
}

/**
 * movedOn lists the instances a move of a link's target moves on: where
 * the link pins what the move made (pin, as Links' linkPin reads the
 * link), the stale ones, and every one linked to it otherwise.
 */
export function movedOn(scope: BehaviorScope<unknown>, link: string, pin: LinkPin | undefined, id: string, move: TargetMove): string[] {
  return linkedTo(scope, link, id, pin === move.kind);
}
