/*
Links' guidance: each link by name, the schema it points at, and whether
it is required, pinned to a revision or a release, or holds a list of
targets, so a create knows which links it must give and link and unlink
which names they take; and the narrowed create parameters: one property
per link name, the required ones required, a revision only for a link
that pins revisions and a release only for one that pins releases, and a
list of at most max targets for a many-valued link, at least one when it
is required.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { JSONSchema } from '../../declaration.js';
import type { LinkPin, LinkSpec, LinksConfig } from '../links.js';
import { list, sentences } from './text.js';

const ID_PATTERN = '^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$';

// described says one link: its name, its schema, and how it holds.
function described(name: string, link: LinkSpec): string {
  const how = [
    link.required ? 'required' : undefined,
    link.pin === undefined ? undefined : `pinned to a ${link.pin}`,
    link.many ? `a list of at most ${link.max}` : undefined,
  ].filter((part) => part !== undefined);
  return `${name} to ${link.schema}${how.length > 0 ? ` (${how.join(', ')})` : ''}`;
}

// pinsOf lists the links that pin one kind.
function pinsOf(config: LinksConfig, pin: LinkPin): string[] {
  return Object.keys(config.links).filter((name) => config.links[name].pin === pin);
}

// verb is a verb for one name or several: holds, hold.
function verb(names: readonly string[], one: string, many: string): string {
  return names.length === 1 ? one : many;
}

// said is a guidance text, or undefined when it says nothing.
function said(text: string): string | undefined {
  return text === '' ? undefined : text;
}

export function linksGuidance(config: LinksConfig, target: DescribeTarget): BehaviorGuidance {
  const names = Object.keys(config.links);
  const many = names.filter((name) => config.links[name].many);
  const single = names.filter((name) => !config.links[name].many);
  const required = names.filter((name) => config.links[name].required);
  const requiredOne = required.filter((name) => !config.links[name].many);
  const requiredMany = required.filter((name) => config.links[name].many);
  const optionalOne = single.filter((name) => !config.links[name].required);
  const revisions = pinsOf(config, 'revision');
  const releases = pinsOf(config, 'release');
  const pinned = [...revisions, ...releases];
  const noPin = [
    ...(revisions.length > 0 ? [{ code: 'no_revision', commonCorrection: 'Wait until the target has a revision, or link a target that has one.' }] : []),
    ...(releases.length > 0 ? [{ code: 'no_release', commonCorrection: 'Wait until the target has been released, or link a target that has.' }] : []),
  ];
  const records = (links: readonly string[], what: string) =>
    links.length > 0 ? `${list(links)} record${links.length === 1 ? 's' : ''} the target's latest ${what}, or the one given.` : undefined;
  const selfRequired = (names: readonly string[]) => names.filter((name) => config.links[name].schema === target.schema);
  const selfOne = selfRequired(requiredOne);
  const selfMany = selfRequired(requiredMany);
  return {
    summary: sentences(
      `Each ${target.type} links to other instances by name: ${list(names.map((name) => described(name, config.links[name])))}.`,
      requiredOne.length > 0
        ? `Every create gives ${list(requiredOne)}; a required link is moved, never unlinked, and the instance it points at cannot be deleted.`
        : undefined,
      many.length > 0
        ? `${list(many)} ${verb(many, 'holds', 'hold')} a list of targets in behaviors.Links.targets, in the order they were linked: link adds one and unlink removes the one it names.`
        : undefined,
      requiredMany.length > 0
        ? `Every create gives at least one target of ${list(requiredMany)}, which ${verb(requiredMany, 'keeps', 'keep')} one: the last target can be neither unlinked nor deleted.`
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
        useWhen: sentences(
          single.length > 0 ? `Use to point ${list(single, 'or')} at an instance of its schema, moving it when it is set.` : undefined,
          many.length > 0 ? `Use to add a target to ${list(many, 'or')}, or to pin again one it holds.` : undefined,
          records(revisions, 'revision'),
          records(releases, 'release')
        ),
        doNotUseWhen: 'Do not use at create; give the links in behaviors.Links instead.',
        success: 'Returns the link: its name, schema, id and the revision or release a pinned link records.',
        errors: [...noPin, ...(many.length > 0 ? [{ code: 'too_many', commonCorrection: 'Unlink a target first.' }] : [])],
      },
      unlink: {
        useWhen: said(
          sentences(
            optionalOne.length > 0 ? `Use to clear ${list(optionalOne, 'or')}.` : undefined,
            many.length > 0 ? `Use with the target's id to remove one target of ${list(many, 'or')}.` : undefined
          )
        ),
        doNotUseWhen: said(
          sentences(
            requiredOne.length > 0 ? `Do not use on ${list(requiredOne, 'or')}: a required link is moved with link, never unlinked.` : undefined,
            requiredMany.length > 0 ? `Do not remove the last target of ${list(requiredMany, 'or')}: a required link keeps one.` : undefined
          )
        ),
        success: 'Returns the link as it was.',
        errors: [
          ...(required.length > 0
            ? [
                {
                  code: 'required_link',
                  commonCorrection: sentences(
                    requiredOne.length > 0 ? 'Move the link with link instead.' : undefined,
                    requiredMany.length > 0 ? 'Link another target before unlinking the last.' : undefined
                  ),
                },
              ]
            : []),
          ...(many.length > 0 ? [{ code: 'not_linked', commonCorrection: 'Read behaviors.Links.targets for the targets the link holds.' }] : []),
        ],
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
          many.length > 0 ? `Give ${list(many.map((name) => `${name} a list of up to ${config.links[name].max}`))}, each target once.` : undefined,
          required.length > 0 ? `${list(required)} ${required.length === 1 ? 'is' : 'are'} required.` : undefined
        ),
        errors: noPin,
      },
      ...(selfOne.length > 0 || selfMany.length > 0
        ? {
            delete: {
              doNotUseWhen: sentences(
                selfOne.length > 0 ? `Do not delete an instance that the ${list(selfOne, 'or')} link of another ${target.type} points at.` : undefined,
                selfMany.length > 0 ? `Do not delete the last target of the ${list(selfMany, 'or')} link of another ${target.type}.` : undefined
              ),
              errors: [{ code: 'required_target', commonCorrection: 'Move the required links that point at it first.' }],
            },
          }
        : {}),
    },
  };
}

// targetSchema is the schema of one target a create gives a link: its id,
// or an object with its id and the pin to record.
function targetSchema(link: LinkSpec, description: string): Record<string, unknown> {
  const pin = link.pin;
  return {
    description,
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

/**
 * linksCreateParams is Links' create parameters under its config: a
 * property per link name, the required ones required, a revision only for
 * a link that pins revisions and a release only for one that pins
 * releases, and for a many-valued link a list of at most max targets, at
 * least one when it is required.
 */
export function linksCreateParams(config: LinksConfig): JSONSchema {
  const properties: Record<string, unknown> = {};
  for (const [name, link] of Object.entries(config.links)) {
    const pin = link.pin;
    const what = `its id${pin === undefined ? '' : `, or an object with its id and the ${pin} to record, its latest when absent`}`;
    properties[name] = link.many
      ? {
          description: `The ${link.schema} instances ${name} points at, in order, each once: ${what}.`,
          type: 'array',
          ...(link.required ? { minItems: 1 } : {}),
          maxItems: link.max,
          items: targetSchema(link, `One ${link.schema} instance ${name} points at: ${what}.`),
        }
      : targetSchema(link, `The ${link.schema} instance ${name} points at: ${what}.`);
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
