import { Identity } from "superscalar";

export abstract class Broken {
  id: Identity.DoesNotExist;
}
