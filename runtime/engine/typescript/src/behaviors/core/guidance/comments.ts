/*
Comments' guidance: a thread on each instance, which a caller adds to
and pages through. It takes no config, so its text is the same on every
type.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';

export function commentsGuidance(_config: unknown, target: DescribeTarget): BehaviorGuidance {
  return {
    summary: `Each ${target.type} holds a thread of comments, each one a reply to another of its comments or not; commentCount counts them.`,
    operations: {
      comment: {
        useWhen: "Use to add a comment to the instance, or with replyTo a reply to one of the instance's comments.",
        doNotUseWhen: "Do not use to change the instance's fields; call update.",
        success: 'Returns the comment with its id, which a reply names as replyTo; commentCount grows by one.',
      },
      listComments: {
        useWhen: "Use to read the instance's comments, oldest first, a page at a time.",
        success: 'Returns items and next; pass next as cursor for the page after, until it is null.',
      },
    },
  };
}
