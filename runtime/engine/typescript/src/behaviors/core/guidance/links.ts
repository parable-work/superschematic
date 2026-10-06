/*
Links' guidance: each link by name, the schema it points at, and whether
it is required or pinned, so a create knows which links it must give
and link and unlink which names they take; and the narrowed create
parameters: one property per link name, the required ones required, a
revision only for a pinned link.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { JSONSchema } from '../../declaration.js';
import type { LinkSpec, LinksConfig } from '../links.js';
import { list, sentences } from './text.js';

const ID_PATTERN = '^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$';

// described says one link: its name, its schema, and how it holds.
function described(name: string, link: LinkSpec): string {
  const how = [link.required ? 'required' : undefined, link.pinned ? 'pinned to a revision' : undefined].filter((part) => part !== undefined);
  return `${name} to ${link.schema}${how.length > 0 ? ` (${how.join(', ')})` : ''}`;
}

export function linksGuidance(config: LinksConfig, target: DescribeTarget): BehaviorGuidance {
  const names = Object.keys(config.links);
  const required = names.filter((name) => config.links[name].required);
  const optional = names.filter((name) => !config.links[name].required);
  const pinned = names.filter((name) => config.links[name].pinned);
  const noRevision = {
    code: 'no_revision',
    commonCorrection: 'Wait until the target has a revision, or link a target that has one.',
  };
  const selfRequired = required.filter((name) => config.links[name].schema === target.schema);
  return {
    summary: sentences(
      `Each ${target.type} links to other instances by name: ${list(names.map((name) => described(name, config.links[name])))}.`,
      required.length > 0
        ? `Every create gives ${list(required)}; a required link is moved, never unlinked, and the instance it points at cannot be deleted.`
        : undefined,
      pinned.length > 0 ? `A pinned link records the target's revision; links says stale once the target has a newer one.` : undefined
    ),
    operations: {
      link: {
        useWhen: sentences(
          `Use to point ${list(names, 'or')} at an instance of its schema, moving it when it is set.`,
          pinned.length > 0 ? `${list(pinned)} record${pinned.length === 1 ? 's' : ''} the target's latest revision, or the one given.` : undefined
        ),
        doNotUseWhen: 'Do not use at create; give the links in behaviors.Links instead.',
        success: 'Returns the link: its name, schema, id and the revision a pinned link records.',
        errors: pinned.length > 0 ? [noRevision] : [],
      },
      unlink: {
        useWhen: optional.length > 0 ? `Use to clear ${list(optional, 'or')}.` : undefined,
        doNotUseWhen: required.length > 0 ? `Do not use on ${list(required, 'or')}: a required link is moved with link, never unlinked.` : undefined,
        success: 'Returns the link as it was.',
        errors: required.length > 0 ? [{ code: 'required_link', commonCorrection: 'Move the link with link instead.' }] : [],
      },
      listLinked: {
        useWhen: sentences(
          `Use to find the ${target.type} instances whose link points at a target.`,
          pinned.length > 0 ? 'With stale true, only the pinned ones the target has moved past.' : undefined
        ),
        success: 'Returns items and next; pass next as cursor for the page after, until it is null.',
      },
      create: {
        useWhen: sentences(
          `behaviors.Links gives the links from the create, by name (${list(names)}): each the target's id${pinned.length > 0 ? `, or { id, revision } for ${list(pinned)}` : ''}.`,
          required.length > 0 ? `${list(required)} ${required.length === 1 ? 'is' : 'are'} required.` : undefined
        ),
        errors: pinned.length > 0 ? [noRevision] : [],
      },
      ...(selfRequired.length > 0
        ? {
            delete: {
              doNotUseWhen: `Do not delete an instance that the ${list(selfRequired, 'or')} link of another ${target.type} points at.`,
              errors: [{ code: 'required_target', commonCorrection: 'Move the required links that point at it first.' }],
            },
          }
        : {}),
    },
  };
}

/**
 * linksCreateParams is Links' create parameters under its config: a
 * property per link name, the required ones required, a revision only for
 * a pinned link.
 */
export function linksCreateParams(config: LinksConfig): JSONSchema {
  const properties: Record<string, unknown> = {};
  for (const [name, link] of Object.entries(config.links)) {
    properties[name] = {
      description: `The ${link.schema} instance ${name} points at: its id${link.pinned ? ', or an object with its id and the revision to record, its latest when absent' : ''}.`,
      type: ['string', 'object'],
      pattern: ID_PATTERN,
      additionalProperties: false,
      required: ['id'],
      properties: {
        id: { description: "The target's id.", type: 'string', pattern: ID_PATTERN },
        ...(link.pinned ? { revision: { description: "The target's revision to record, one it has had.", type: 'integer', minimum: 1 } } : {}),
      },
    };
  }
  const required = Object.keys(config.links).filter((name) => config.links[name].required);
  return {
    description: 'The links the instance holds from its create, by name, each held to link\'s rules.',
    type: 'object',
    additionalProperties: false,
    properties,
    ...(required.length > 0 ? { required } : {}),
  };
}
