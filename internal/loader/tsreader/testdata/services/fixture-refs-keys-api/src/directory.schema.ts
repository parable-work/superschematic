import { Identity } from "superscalar";
import { Nullable, display } from "@superschematic/schema";
import { HttpMethod, rest, source } from "@superschematic/api";
import { Tenant, TenantUser } from "@schemas/fixture-db";

// A tenant as the directory lists it, named by its name.
@source(Tenant)
@display({ noun: "Tenant", plural: "Tenants", titleField: "name" })
export abstract class TenantView {
  id: Identity.UUID;
  name: Identity.Name;
}

// A member of a tenant. tenantId reads the key of TenantUser.tenant, a
// Relation to Tenant, and so refers to TenantView, which listTenants lists.
@source(TenantUser)
export abstract class TenantUserView {
  id: Identity.UUID;
  displayName: Nullable<string>;
  tenantId: Identity.UUID;
}

export class Directory {
  @rest(HttpMethod.GET, "tenants")
  listTenants(): TenantView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "users")
  listUsers(): TenantUserView[] {
    throw new Error("schema declaration only");
  }
}
