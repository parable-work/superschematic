import { Identity } from "@psgen/scalar-lib";

export abstract class Broken {
  id: Identity.DoesNotExist;
}
