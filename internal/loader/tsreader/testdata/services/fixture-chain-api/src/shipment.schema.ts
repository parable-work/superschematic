import { Identity } from "superscalar";
import { HttpMethod, rest } from "@superschematic/api";
import { Address } from "@schemas/fixture-chain-common";

export abstract class CreateShipmentInput {
  address: Address;
}

export abstract class ShipmentView {
  id: Identity.UUID;
  address: Address;
}

export class ShipmentMutations {
  // Ship to an address.
  @rest(HttpMethod.POST, "shipments")
  createShipment(input: CreateShipmentInput): ShipmentView {
    throw new Error("schema declaration only");
  }
}
