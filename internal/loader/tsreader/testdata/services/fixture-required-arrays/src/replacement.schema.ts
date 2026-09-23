// A required array means present, not non-empty. RequiredReplacement has no
// listMin, so an explicit [] is valid and only an absent or null list fails.
export abstract class RequiredAssignment {
  kind: string;
}

export abstract class RequiredReplacement {
  expectedAggregateRevision: number;
  assignments: RequiredAssignment[];
}
