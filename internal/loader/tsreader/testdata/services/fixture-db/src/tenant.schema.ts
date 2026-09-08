import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate } from "@superschematic/schema";
import {
  AutoGenerate,
  HasMany,
  JsonField,
  Relation,
  index,
  jsonField,
  key,
  searchField,
  sourceMustProject,
  unique,
  versioned
} from "@superschematic/db";

// Base class providing audit fields to every table.
export abstract class Auditable {
  createdAt: Temporal.DateTime;
  updatedAt: Nullable<Temporal.DateTime>;
}

export enum TenantStatus {
  Active = "active",
  Suspended = "suspended"
}

// A tenant of the platform.
@versioned
@index<Tenant>(["slug"], { unique: true, name: "slug" })
export abstract class Tenant extends Auditable {
  @unique
  @key
  id: AutoGenerate<Identity.UUID>;

  // Display name shown across the product.
  @searchField
  @sourceMustProject
  name: Identity.Name;

  @unique
  slug: Identity.Slug;

  email: Validate<string, { pattern: "@" }>;
  status: Default<TenantStatus, TenantStatus.Active>;
  isActive: Default<boolean, true>;
  seatCount: Default<number, 5>;

  @jsonField
  metadata: JsonField<Generic.JSON>;

  users: HasMany<TenantUser>;
}

// A soft-deletable tenant membership.
@versioned
@index<TenantUser>(["tenant", "displayName"], true)
export abstract class TenantUser extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;
  tenant: Relation<Tenant, { onDelete: "RESTRICT" }>;
  displayName: Nullable<string>;
  deletedAt: Nullable<Temporal.DateTime>;
  deletedBy: Nullable<Identity.UUID>;
}
