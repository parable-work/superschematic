// A base class another service extends. Its fields reference a type and an
// enum declared here, so a service that flattens it depends on this one.
export enum StampStage {
  Draft = "draft",
  Final = "final"
}

export type StampOrigin = {
  readonly system: string;
};

export abstract class Stamped {
  stage: StampStage;
  origin: StampOrigin;
}
