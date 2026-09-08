import { Trait } from "@psgen/schema";

// Naming a non-trait class through the Trait<T> carrier passes the compiler
// (Trait<T> erases to an empty object type) but must fail verification:
// only @trait classes are implementable.
export abstract class Plain {
  status: string;
}

export abstract class Gadget implements Trait<Plain> {
  title: string;
}
