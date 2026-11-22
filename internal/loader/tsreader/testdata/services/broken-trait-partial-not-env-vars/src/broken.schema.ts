import { Trait, trait } from "@superschematic/schema";

// Trait<Partial<T>> makes every composed field optional. Only an @envVars
// class may use it, so a data type cannot loosen a trait's fields.
@trait()
export abstract class Stamped {
  stampedBy: string;
}

export abstract class Gadget implements Trait<Partial<Stamped>> {
  title: string;
}
