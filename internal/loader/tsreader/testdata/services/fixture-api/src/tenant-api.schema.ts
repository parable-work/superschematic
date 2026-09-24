import { Identity } from "superscalar";
import { Nullable, Secret, Validate } from "@superschematic/schema";
import {
  Authenticated,
  Encrypted,
  EncryptedField,
  HttpMethod,
  QueryParam,
  auth,
  bodyLimit,
  manualRouteRegistration,
  rateLimit,
  requireOwnership,
  requirePermission,
  rest,
  source,
  timeout,
  uiHidden,
  virtual
} from "@superschematic/api";
import { Tenant } from "@schemas/fixture-db";

// Status filter for listTenants: an enum array query parameter.
export enum TenantListStatus {
  Active = "active",
  Suspended = "suspended"
}

// Customer-facing projection of the Tenant table.
@source(Tenant)
export abstract class TenantView {
  id: Identity.UUID;
  name: Identity.Name;

  @virtual
  userCount: number;

  @uiHidden
  @virtual
  internalDebugLabel: string;
}

export abstract class CreateTenantInput {
  name: Identity.Name;
  slug: Identity.Slug;
}

@rateLimit({ requestsPerMinute: 60 })
@bodyLimit({ megabytes: 1 })
export class TenantQueries extends Authenticated {
  // Fetch one tenant by id.
  @rest(HttpMethod.GET, "tenants/{id}")
  @requirePermission(["tenants.read"])
  @timeout({ seconds: 5 })
  getTenant(id: Identity.UUID, includeArchived: QueryParam<boolean>): TenantView {
    throw new Error("schema declaration only");
  }

  // Array query parameters: ?ids=a,b&statuses=active,suspended.
  @rest(HttpMethod.GET, "tenants")
  @requirePermission(["tenants.read"])
  listTenants(
    ids: QueryParam<Validate<Identity.UUID[], { listMin: 1; listMax: 100 }>>,
    statuses: QueryParam<Nullable<Validate<TenantListStatus[], { listMin: 0; listMax: 10 }>>>
  ): TenantView[] {
    throw new Error("schema declaration only");
  }
}

export class SessionQueries {
  @rest(HttpMethod.GET, "auth/me")
  @auth
  currentTenant(): TenantView {
    throw new Error("schema declaration only");
  }
}

export class TenantMutations extends Encrypted {
  @rest(HttpMethod.POST, "tenants")
  @requirePermission(["tenants.write"])
  createTenant(input: CreateTenantInput): TenantView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.PATCH, "tenants/{id}")
  @requirePermission(["tenants.write"])
  @requireOwnership
  updateSecret(id: Identity.UUID, secret: EncryptedField<Secret<string>>): TenantView {
    throw new Error("schema declaration only");
  }

  @manualRouteRegistration
  customHandler(): TenantView {
    throw new Error("schema declaration only");
  }
}
