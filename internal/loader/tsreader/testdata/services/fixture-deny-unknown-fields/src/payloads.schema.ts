import { denyUnknownFields } from "@superschematic/schema";

// A closed wire contract: a misspelled key is an error, not a silent drop.
@denyUnknownFields
export abstract class StrictPayload {
  name: string;
}

// An open payload: keys from a newer producer are ignored.
export abstract class LenientPayload {
  name: string;
}
