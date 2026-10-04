/*
Assignment, who an instance is assigned to (D16): one principal at a
time, or none. A principal may assign an unassigned instance to itself,
and an assignee may unassign itself. Assigning another principal,
reassigning, and unassigning another principal need the config's
permission; without one in the config, only those two self-service moves
exist. The engine knows no principals, so it cannot check that another
principal exists: only a principal trusted with the permission names one.

While the instance is assigned, the guard refuses Lease's acquire and
Queue's claim by every principal but the assignee, whoever made the call
(a claim calls acquire for its caller), so an assigned instance is
claimed only by its assignee.

Every refusal is a veto with a code the declaration lists
(assigned_to_another, already_assigned, not_assigned, not_configured),
but a move the config's permission allows and the caller lacks, which is
forbidden.

configChange: the permission may change. Assignment can be added to a
schema that has instances, which start unassigned, and cannot be removed
from one: the assignments they hold would stay behind and come back if it
were added again.
*/

import { BehaviorVetoError, EngineError, defineBehavior, type BehaviorScope, type InstanceView } from '@superschematic/engine';

import declaration from './declarations/Assignment.behavior.json' with { type: 'json' };

/** Assignment's config. */
export interface AssignmentConfig {
  /** The permission that assigns another principal, reassigns, and unassigns another principal. */
  readonly permission?: string;
}

const NAME = 'Assignment';

/** The codes Assignment's vetoes carry, as its declaration lists them. */
type AssignmentVeto = 'assigned_to_another' | 'already_assigned' | 'not_assigned' | 'not_configured';

function vetoed(view: InstanceView<unknown>, operation: string, reason: string, code: AssignmentVeto): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, { reason, code });
}

/** The operations an assignment keeps to its assignee: taking the lease, and claiming the instance. */
const GATED = new Set(['Lease.acquire', 'Queue.claim']);

function assigneeOf(view: InstanceView<AssignmentConfig>): string | null {
  const assignee = view.columns.get().assignee;
  return assignee === null || assignee === undefined ? null : String(assignee);
}

function privileged(scope: BehaviorScope<AssignmentConfig>): boolean {
  const permission = scope.config.permission;
  return permission !== undefined && scope.can(permission);
}

// refused is the refusal of a move only the permission allows: forbidden
// when the config names one the caller lacks, vetoed when it names none.
function refused(view: InstanceView<AssignmentConfig>, operation: string, what: string): Error {
  const permission = view.config.permission;
  if (permission === undefined) {
    return vetoed(view, operation, `${what} needs a permission, and its config names none`, 'not_configured');
  }
  return new EngineError('forbidden', `${view.principal.subject} may not ${operation} ${view.schema} ${view.id}: ${what} needs permission ${permission}`);
}

export const assignment = defineBehavior<AssignmentConfig>({
  declaration,

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the assignments its instances hold would stay behind';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'assignment',
      columns: {
        assignee: { type: 'text' },
        assigned_at: { type: 'integer' },
        assigned_by: { type: 'text' },
      },
    },
  ],

  guard(view, request) {
    if (request.kind !== 'operation' || !GATED.has(`${request.behavior}.${request.operation}`)) {
      return undefined;
    }
    const assignee = assigneeOf(view);
    if (assignee === null || assignee === view.principal.subject) {
      return undefined;
    }
    return { reason: 'it is assigned to another principal, who alone may take it', code: 'assigned_to_another' };
  },

  operations: {
    assign(context, params) {
      const to = params.to as string;
      const subject = context.principal.subject;
      const current = assigneeOf(context);
      if (current === to) {
        throw vetoed(context, 'assign', to === subject ? 'it is already assigned to the caller' : 'it is already assigned to that principal', 'already_assigned');
      }
      if (!privileged(context)) {
        if (to !== subject) {
          throw refused(context, 'assign', 'assigning another principal');
        }
        if (current !== null) {
          throw refused(context, 'assign', 'reassigning an instance assigned to another principal');
        }
      }
      context.columns.set({ assignee: to, assigned_at: context.now, assigned_by: subject });
      return { assignee: to, assignedAt: context.now, assignedBy: subject };
    },

    unassign(context) {
      const current = assigneeOf(context);
      if (current === null) {
        throw vetoed(context, 'unassign', 'it is not assigned', 'not_assigned');
      }
      if (current !== context.principal.subject && !privileged(context)) {
        throw refused(context, 'unassign', 'unassigning another principal');
      }
      context.columns.set({ assignee: null, assigned_at: null, assigned_by: null });
      return { assignee: current };
    },
  },

  fields: {
    assignee: (view) => assigneeOf(view) ?? undefined,
  },
});
