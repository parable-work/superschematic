/*
Variants' guidance: which type a field holds for each value of another,
so a create or an update gives the field the shape that value picks.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { VariantsConfig } from '../variants.js';
import { list } from './text.js';

export function variantsGuidance(config: VariantsConfig, target: DescribeTarget): BehaviorGuidance {
  const values = Object.keys(config.types);
  const shapes = values.map((value) => `${config.types[value]} when ${config.by} is ${value}`);
  const rule = `${config.field} holds ${list(shapes, 'or')}, and nothing while ${config.by} holds another value or none`;
  return {
    summary: `On each ${target.type}, ${config.field} takes the shape ${config.by} picks: ${rule}.`,
    operations: {
      create: { useWhen: `Give ${config.field} the shape ${config.by} picks: ${rule}.` },
      update: { useWhen: `A patch of ${config.field} keeps the shape ${config.by} picks, as merged; the describe document shows each shape under allOf.` },
    },
  };
}
