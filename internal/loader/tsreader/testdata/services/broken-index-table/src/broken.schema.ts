import { Identity } from "superscalar";
import { Nullable, Trait, jsonField, trait } from "@superschematic/schema";
import { AutoGenerate, index, key } from "@superschematic/db";

// A base class gets no table: its fields are copied onto Tenant and Member,
// its indexes are not.
@index<Auditable>(["createdAt"])
export abstract class Auditable {
  createdAt: string;
}

// Stored as JSON in Tenant's address column, so it gets no table.
@jsonField
@index<Address>(["city"])
export abstract class Address {
  city: string;
}

// A trait's fields are copied onto each implementing table; its indexes are
// not.
@trait()
@index<SoftDeletable>(["deletedAt"])
export abstract class SoftDeletable {
  deletedAt: Nullable<string>;
}

export abstract class Tenant extends Auditable implements Trait<SoftDeletable> {
  @key
  id: AutoGenerate<Identity.UUID>;
  address: Address;
}

export abstract class Member extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}
