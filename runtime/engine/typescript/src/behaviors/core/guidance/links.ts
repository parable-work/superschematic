/*
Links' guidance: each link by name, the schema it points at, and whether
it is required or pinned to a revision or a release, so a create knows
which links it must give and link and unlink which names they take; and
the narrowed create parameters: one property per link name, the
required ones required, a revision only for a link that pins revisions
and a release only for one that pins releases.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { JSONSchema } from '../../declaration.js';
import type { LinkPin, LinkSpec, LinksConfig } from '../links.js';
import { list, sentences } from './text.js';

const ID_PATTERN = '^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$';

// described says one link: its name, its schema, and how it holds.
function described(name: string, link: LinkSpec): string {
  const how = [link.required ? 'required' : undefined, link.pin === undefined ? undefined : `pinned to a ${link.pin}`].filter((part) => part !== undefined);
  return `${name} to ${link.schema}${how.length > 0 ? ` (${how.join(', ')})` : ''}`;
}

// pinsOf lists the links that pin one kind.
function pinsOf(config: LinksConfig, pin: LinkPin): string[] {
  return Object.keys(config.links).filter((name) => config.links[name].pin === pin);
}

export function linksGuidance(config: LinksConfig, target: DescribeTarget): BehaviorGuidance {
  const names = Object.keys(config.links);
  const required = names.filter((name) => config.links[name].required);
  const optional = names.filter((name) => !config.links[name].required);
  const revisions = pinsOf(config, 'revision');
  const releases = pinsOf(config, 'release');
  const pinned = [...revisions, ...releases];
  const noPin = [
    ...(revisions.length > 0 ? [{ code: 'no_revision', commonCorrection: 'Wait until the target has a revision, or link a target that has one.' }] : []),
    ...(releases.length > 0 ? [{ code: 'no_release', commonCorrection: 'Wait until the target has been released, or link a target that has.' }] : []),
  ];
  const records = (links: readonly string[], what: string) =>
    links.length > 0 ? `${list(links)} record${links.length === 1 ? 's' : ''} the target's latest ${what}, or the one given.` : undefined;
  const selfRequired = required.filter((name) => config.links[name].schema === target.schema);
  return {
    summary: sentences(
      `Each ${target.type} links to other instances by name: ${list(names.map((name) => described(name, config.links[name])))}.`,
      required.length > 0
        ? `Every create gives ${list(required)}; a required link is moved, never unlinked, and the instance it points at cannot be deleted.`
        : undefined,
      revisions.length > 0
        ? "A link pinned to a revision records the target's revision; behaviors.Links.targets gives the target's latest beside it and says stale once the target has a newer one."
        : undefined,
      releases.length > 0
        ? "A link pinned to a release records the target's release; behaviors.Links.targets gives the target's latest beside it and says stale once the target has released again."
        : undefined
    ),
    operations: {
      link: {
        useWhen: sentences(`Use to point ${list(names, 'or')} at an instance of its schema, moving it when it is set.`, records(revisions, 'revision'), records(releases, 'release')),
        doNotUseWhen: 'Do not use at create; give the links in behaviors.Links instead.',
        success: 'Returns the link: its name, schema, id and the revision or release a pinned link records.',
        errors: noPin,
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
          `behaviors.Links gives the links from the create, by name (${list(names)}): each the target's id${
            revisions.length > 0 ? `, or { id, revision } for ${list(revisions)}` : ''
          }${releases.length > 0 ? `, or { id, release } for ${list(releases)}` : ''}.`,
          required.length > 0 ? `${list(required)} ${required.length === 1 ? 'is' : 'are'} required.` : undefined
        ),
        errors: noPin,
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
 * a link that pins revisions and a release only for one that pins
 * releases.
 */
export function linksCreateParams(config: LinksConfig): JSONSchema {
  const properties: Record<string, unknown> = {};
  for (const [name, link] of Object.entries(config.links)) {
    const pin = link.pin;
    properties[name] = {
      description: `The ${link.schema} instance ${name} points at: its id${pin === undefined ? '' : `, or an object with its id and the ${pin} to record, its latest when absent`}.`,
      type: ['string', 'object'],
      pattern: ID_PATTERN,
      additionalProperties: false,
      required: ['id'],
      properties: {
        id: { description: "The target's id.", type: 'string', pattern: ID_PATTERN },
        ...(pin === undefined ? {} : { [pin]: { description: `The target's ${pin} to record, one it has had.`, type: 'integer', minimum: 1 } }),
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
