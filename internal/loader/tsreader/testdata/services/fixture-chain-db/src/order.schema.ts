import { Identity } from "superscalar";
import { AutoGenerate, JsonField, jsonField, key } from "@superschematic/db";
import { Address } from "@schemas/fixture-chain-common";

// An order whose JSONB column holds a fixture-chain-common Address.
export abstract class ChainOrder {
  @key
  id: AutoGenerate<Identity.UUID>;

  @jsonField
  shippingAddress: JsonField<Address>;
}
