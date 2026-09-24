import { strictJSON } from "@superschematic/schema";

// A closed contract: every decoder rejects an undeclared key and an absent
// or null required field.
@strictJSON
export abstract class Policy {
  grant: Grant;
}

// Nested object types opt in on their own.
@strictJSON
export abstract class Grant {
  members: string[];
}

// An open payload: keys from a newer producer are ignored.
export abstract class Ordinary {
  name: string;
}
