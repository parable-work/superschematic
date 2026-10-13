/*
Timers' guidance: when each deadline is due, what its fire moves and
what cancels it, how a reader tells one is overdue, and who may reset
or snooze one, so a caller knows what a create, a transition or an
update starts; create, transition and update hear the same.
*/

import type { BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance } from '../../behavior.js';
import type { TimersConfig, TimersDeadline } from '../timers.js';
import { capital, duration, list, sentences } from './text.js';

// due says when a deadline is due, from what it starts from.
function due(deadline: TimersDeadline): string {
  const { start, afterMs } = deadline;
  const offset = afterMs === 0 ? '' : `${duration(Math.abs(afterMs))} ${afterMs < 0 ? 'before' : 'after'} `;
  if ('create' in start) {
    return afterMs === 0 ? 'at the create' : `${offset}the create`;
  }
  if ('enters' in start) {
    return afterMs === 0 ? `when the status enters ${start.enters}` : `${offset}the status enters ${start.enters}`;
  }
  return `${afterMs === 0 ? 'at' : offset}the time ${start.field} holds`;
}

// deadlineText says one deadline as a sentence: when it is due, its move
// and what cancels it.
function deadlineText(name: string, deadline: TimersDeadline): string {
  const parts = [`deadline ${name} is due ${due(deadline)}`];
  if (deadline.then !== undefined) {
    parts.push(`when it fires, it moves the status to ${deadline.then.transition} from ${list(deadline.then.from, 'or')}`);
  }
  if (deadline.until !== undefined) {
    parts.push(
      'leaves' in deadline.until
        ? `it is cancelled when the status leaves ${deadline.until.leaves}`
        : `it is cancelled when the status enters ${list(deadline.until.enters, 'or')}`
    );
  }
  return `${capital(parts.join('; '))}.`;
}

// later says how long after its start a deadline is due, for a caller of
// the write that starts it.
function later(deadline: TimersDeadline): string {
  return deadline.afterMs === 0 ? 'due at once' : `due ${duration(deadline.afterMs)} later`;
}

export function timersGuidance(config: TimersConfig, target: DescribeTarget): BehaviorGuidance {
  const deadlines = Object.entries(config.deadlines);
  const names = deadlines.map(([name]) => name);
  const { permission } = config;
  const noDeadline: GuidanceError = { code: 'no_deadline', commonCorrection: `Name a deadline: ${list(names, 'or')}.` };
  const forbidden = permission === undefined ? undefined : `It needs permission ${permission}, else the call is forbidden.`;
  const closed = 'Do not use: the config names no permission, so the call is forbidden for every caller.';
  const notArmed = (correction: string): GuidanceError => ({ code: 'not_armed', commonCorrection: correction });

  // What a create, a transition and an update start or cancel.
  const created = deadlines
    .filter(([, deadline]) => 'create' in deadline.start)
    .map(([name, deadline]) => `A new ${target.type} starts deadline ${name}, ${later(deadline)}.`);
  const fields = deadlines.flatMap(([name, deadline]) => ('field' in deadline.start ? [[name, deadline.start.field] as const] : []));
  const moves = deadlines.flatMap(([name, deadline]) => [
    ...('enters' in deadline.start ? [`Entering ${deadline.start.enters} starts deadline ${name}, ${later(deadline)}.`] : []),
    ...(deadline.until === undefined
      ? []
      : [
          'leaves' in deadline.until
            ? `Leaving ${deadline.until.leaves} cancels deadline ${name}.`
            : `Entering ${list(deadline.until.enters, 'or')} cancels deadline ${name}.`,
        ]),
  ]);

  const operations: Record<string, OperationGuidance> = {
    fireTimer: {
      useWhen: "Use to fire a due deadline at once rather than wait for the runner's sweep.",
      doNotUseWhen: 'Do not use to move the instance early: on a deadline not due, fired already or not started, the call changes nothing.',
      success:
        'Returns fired, whether this call fired the deadline, and with a then the outcome of its move: moved, skipped (the status was not one it moves from), vetoed or forbidden; the deadline is fired whichever.',
      errors: [noDeadline],
    },
    resetTimer: {
      useWhen: permission === undefined ? '' : `Use to start a deadline again from what it starts from now, undoing a snooze or a fire. ${forbidden}`,
      doNotUseWhen: permission === undefined ? closed : 'Do not use to put a deadline off by a time of your choosing; snoozeTimer takes the dueAt.',
      success: 'Returns dueAt, afterMs after what the deadline starts from; it has not fired.',
      errors: [
        noDeadline,
        notArmed('Wait until the instance holds what the deadline starts from, the state it enters or a date-time in its field, and is out of the states that cancel it.'),
      ],
    },
    snoozeTimer: {
      useWhen:
        permission === undefined ? '' : `Use to move a started deadline's due time to dueAt, in epoch milliseconds; a fired one fires again then. ${forbidden}`,
      doNotUseWhen:
        permission === undefined ? closed : 'Do not use on a deadline that has not started or was cancelled; behaviors.Timers.deadlines lists the started ones.',
      success: 'Returns dueAt. A later write keeps it, until what the deadline starts from changes.',
      errors: [noDeadline, notArmed('None: the deadline has no due time to move. Read behaviors.Timers.deadlines.')],
    },
  };
  if (created.length > 0 || fields.length > 0) {
    operations.create = {
      success: sentences(...created, ...fields.map(([name, field]) => `A ${field} the create gives starts deadline ${name}.`)),
    };
  }
  if (moves.length > 0) {
    operations.transition = { success: sentences(...moves) };
  }
  if (fields.length > 0) {
    operations.update = {
      success: sentences(...fields.map(([name, field]) => `Changing ${field} moves deadline ${name}, which fires again when it comes due; clearing ${field} cancels it.`)),
    };
  }
  return {
    summary: sentences(
      ...deadlines.map(([name, deadline]) => deadlineText(name, deadline)),
      `behaviors.Timers.deadlines gives each started deadline's dueAt, fired, firedAt and outcome; one not fired is overdue once its dueAt has passed.`,
      `The runner fires each due deadline within ${duration(config.sweepMs)}, and a deadline stays fired whatever its move meets.`,
      permission === undefined
        ? 'Nobody resets or snoozes a deadline: the config names no permission.'
        : `A caller with permission ${permission} may reset or snooze a deadline.`
    ),
    operations,
  };
}
