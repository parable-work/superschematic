/*
Blueprint's guidance: which children a stamp creates and when, what each
child holds from its parent, and the refusals a stamp meets, which it
adds to the write that stamps: the create for inline steps, the create
or the link of the from link for steps read from a definition. It adds
no operation of its own.
*/

import type { BehaviorGuidance, DescribeTarget, GuidanceError } from '@superschematic/engine';

import type { BlueprintConfig } from '../blueprint.js';
import { list, sentences } from './text.js';

const CORRECTIONS: Readonly<Record<string, string>> = {
  no_dependencies: "None: the child schema's live version must compose Dependencies for steps that come after others.",
  not_constant: "None: the child schema's live version must keep keyField and the copied fields in Constants; details.fields names the ones it does not.",
  no_revision: 'Link a definition that has a revision, or wait until it has one.',
  unreadable: "None: the caller cannot read the definition's revision; a caller who may read it stamps.",
  invalid_steps: 'Fix the steps in the definition, as the message says, and link its new revision.',
  stamped: 'None: the children were stamped from the revision it pins, so the link stays.',
};

function errors(...codes: string[]): GuidanceError[] {
  return codes.map((code) => ({ code, commonCorrection: CORRECTIONS[code] }));
}

export function blueprintGuidance(config: BlueprintConfig, target: DescribeTarget): BehaviorGuidance {
  const holds = sentences(
    `Each child is a ${config.schema} instance whose ${config.keyField} holds its step's key and whose ${config.parentLink} links to its parent`,
    config.copyFields.length > 0 || config.copyLinks.length > 0
      ? `, copying ${list([...config.copyFields, ...config.copyLinks.map((link) => `the ${link} link`)])}`
      : undefined
  ).replace(/ ,/, ',');
  const after = 'a step after others is blocked by their children, so claimable children are claimed in that order';
  const stampErrors = errors('no_dependencies', 'not_constant');
  if (config.from === undefined) {
    const keys = (config.steps ?? []).map((step) => step.key);
    return {
      summary: `The create of a ${target.type} stamps a child per step, ${list(keys)}, a step whose when does not hold left out. ${holds}; ${after}.`,
      operations: {
        create: {
          success: `The create stamps the children (${list(keys)}) in its own write; behaviors.Blueprint.children lists them.`,
          errors: stampErrors,
        },
      },
    };
  }
  const from = config.from;
  const linkErrors = [...errors('stamped', 'no_revision', 'unreadable', 'invalid_steps'), ...stampErrors];
  return {
    summary: `When the ${from.link} link of a ${target.type} is first set, at its create or by link, the steps are read from ${from.field} of the revision it pins, and a child is stamped per step. ${holds}; ${after}. Once stamped, ${from.link} does not move.`,
    operations: {
      create: {
        success: `A create that gives the ${from.link} link stamps its children in the same write; behaviors.Blueprint.children lists them.`,
        errors: errors('no_revision', 'unreadable', 'invalid_steps', 'no_dependencies', 'not_constant'),
      },
      link: {
        doNotUseWhen: `Do not move ${from.link} once the children are stamped.`,
        success: `Setting ${from.link} for the first time stamps the children in the same write.`,
        errors: linkErrors,
      },
    },
  };
}
