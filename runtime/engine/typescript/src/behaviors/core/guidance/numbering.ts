/*
Numbering's guidance: how a number prints, with the format's own
example, what it counts by, that a create takes one and nothing changes
it, how a list finds an instance by it, and when assignNumber is the
call to make.
*/

import type { BehaviorGuidance, DescribeTarget, OperationGuidance } from '../../behavior.js';
import type { NumberingConfig } from '../numbering.js';
import { sentences } from './text.js';

/** The number and the year the guidance prints its example with. */
const EXAMPLE_SEQ = 42;
const EXAMPLE_YEAR = 2026;

// The tokens of a format parseConfig accepted.
const TOKEN = /\{seq(?::([0-9]+))?\}|\{yyyy\}|\{yy\}/g;

// example prints the format's number 42 of 2026, as the engine would.
function example(format: string): string {
  return format.replace(TOKEN, (token: string, width: string | undefined) => {
    if (token === '{yyyy}') {
      return String(EXAMPLE_YEAR);
    }
    if (token === '{yy}') {
      return String(EXAMPLE_YEAR % 100).padStart(2, '0');
    }
    return String(EXAMPLE_SEQ).padStart(width === undefined ? 0 : Number(width), '0');
  });
}

export function numberingGuidance(config: NumberingConfig, target: DescribeTarget): BehaviorGuidance {
  const link = config.scope?.link;
  const yearly = config.reset === 'yearly';
  const printed = example(config.format);
  const dated = /\{yyyy\}|\{yy\}/.test(config.format);
  const count = [
    link === undefined ? `one count for ${target.schema}` : `a count per target of its ${link} link`,
    ...(yearly ? ['starting again at 1 each UTC year'] : []),
  ].join(', ');
  const operations: Record<string, OperationGuidance> = {
    create: {
      success: sentences(
        `The new ${target.type} holds behaviors.Numbering.seq, the next number of its count, and label, the number as ${config.format} prints it.`,
        link === undefined ? undefined : `It is counted in the count of the ${link} target the create gives.`
      ),
    },
    list: {
      useWhen: sentences(
        `where: { "Numbering.label": "${printed}" } finds an instance by the number a person reads, and "Numbering.seq" by its number.`,
        link === undefined ? undefined : `Each ${link} target counts from 1, so a label can match one instance per target.`
      ),
    },
    update: { doNotUseWhen: 'Do not use to change a number, which behaviors.Numbering holds: nothing changes a number once set.' },
    assignNumber: {
      useWhen: `Use on an instance created before ${target.schema} composed Numbering, which holds no number yet; to backfill, invoke it on each in creation order.`,
      doNotUseWhen: 'Do not use to renumber: an instance numbered already keeps its number, and the call changes nothing.',
      success: `Returns seq and label, counted as its create would have counted them${link === undefined ? '' : `, by its ${link} target`}${yearly ? ', in the year it was created' : ''}; behaviors.Numbering holds them from then on.`,
    },
  };
  if (link !== undefined && target.operations.some((operation) => operation.name === 'link')) {
    operations.link = { success: `Moving ${link} keeps the instance's number, which stays in the count of the target it was numbered under.` };
  }
  return {
    summary: sentences(
      `Each ${target.type} gets a number at its create, in ${count}, printed as ${config.format}: number ${EXAMPLE_SEQ}${dated ? `, made in ${EXAMPLE_YEAR},` : ''} prints ${printed}.`,
      link === undefined ? 'A label is unique.' : `A label is unique among one ${link} target's instances.`,
      'A refused create takes no number, a delete leaves a gap, and nothing changes a number once set.'
    ),
    operations,
  };
}
