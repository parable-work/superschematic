/*
Constants' guidance: which fields keep the value their create gave them,
and who may change them after it.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { ConstantsConfig } from '../constants.js';
import { list } from './text.js';

export function constantsGuidance(config: ConstantsConfig, target: DescribeTarget): BehaviorGuidance {
  const one = config.fields.length === 1;
  const who = config.permission === undefined ? 'nothing changes' : `only a caller with permission ${config.permission} changes`;
  return {
    summary: `${list(config.fields)} keep${one ? 's' : ''} the value the create of each ${target.type} gives; after it, ${who} ${one ? 'it' : 'them'}.`,
    operations: {
      create: { useWhen: `Give ${list(config.fields)} at create: ${one ? 'it does' : 'they do'} not change after it, and one left absent stays absent.` },
      update: {
        doNotUseWhen: `Do not change ${list(config.fields, 'or')}${
          config.permission === undefined ? '' : ` without permission ${config.permission}`
        }: a change is refused as invalid_instance, with rule constant at the field.`,
      },
    },
  };
}
