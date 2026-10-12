/*
The identity descriptor (internal/generator/identitydesc/README.md): the
tables a schema's users, sessions, credentials, roles and role grants live
in, and which column of each plays which part. The DB build writes it as
identity/<schema>.json and the TypeScript types export it as
identityDescriptor; a store reads either, the JSON text or its value.
*/

/** The descriptor format the stores read. */
export const DESCRIPTOR_VERSION = 1;

export interface IdentityDescriptor {
  readonly version: 1;
  readonly user: {
    readonly type: string;
    readonly table: string;
    readonly columns: { readonly key: string; readonly login: string; readonly name: string };
    /** The key field's type: a scalar's canonical name, such as Identity.UUID, or a builtin type, such as string. */
    readonly keyScalar: string;
    /** The login field's scalar, such as Contact.Email or Identity.Slug. */
    readonly loginScalar: string;
    /** The name field's type, a scalar such as Identity.Name or a builtin such as string; the login's when the name is the login. */
    readonly nameScalar: string;
  };
  readonly session: {
    readonly table: string;
    readonly columns: {
      readonly id: string;
      readonly user: string;
      readonly tokenHash: string;
      readonly createdAt: string;
      readonly expiresAt: string;
      readonly lastSeenAt: string;
      readonly revokedAt: string;
    };
  };
  readonly credential: {
    readonly table: string;
    readonly columns: {
      readonly id: string;
      readonly user: string;
      readonly passwordHash: string;
      readonly passwordChangedAt: string;
      readonly disabledAt: string;
    };
  };
  /** Absent when the schema has no UserRole table, and roleGrant with it. */
  readonly role?: {
    readonly type: string;
    readonly table: string;
    readonly columns: { readonly key: string; readonly name: string; readonly permissions: string };
    /** The role table's key field's type, as user.keyScalar is the user table's. */
    readonly keyScalar: string;
  };
  readonly roleGrant?: {
    readonly table: string;
    readonly columns: { readonly id: string; readonly user: string; readonly role: string; readonly grantedAt: string };
  };
}

/** A descriptor a store cannot read. */
export class IdentityDescriptorError extends Error {
  constructor(message: string) {
    super(`identity: descriptor: ${message}`);
    this.name = 'IdentityDescriptorError';
  }
}

// Each member's shape: a name (a string), or an object of members.
type Shape = 'name' | { readonly [member: string]: Shape };

const columns = (...names: string[]): Shape => Object.fromEntries(names.map(name => [name, 'name' as const]));

const SHAPE: Record<string, Shape> = {
  user: { type: 'name', table: 'name', columns: columns('key', 'login', 'name'), keyScalar: 'name', loginScalar: 'name', nameScalar: 'name' },
  session: { table: 'name', columns: columns('id', 'user', 'tokenHash', 'createdAt', 'expiresAt', 'lastSeenAt', 'revokedAt') },
  credential: { table: 'name', columns: columns('id', 'user', 'passwordHash', 'passwordChangedAt', 'disabledAt') },
  role: { type: 'name', table: 'name', columns: columns('key', 'name', 'permissions'), keyScalar: 'name' },
  roleGrant: { table: 'name', columns: columns('id', 'user', 'role', 'grantedAt') },
};

function check(value: unknown, shape: Shape, path: string): void {
  if (shape === 'name') {
    if (typeof value !== 'string') throw new IdentityDescriptorError(`${path} must be a string`);
    if (value === '') throw new IdentityDescriptorError(`${path} is empty`);
    if (value.includes('\0')) throw new IdentityDescriptorError(`${path} holds a NUL`);
    return;
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new IdentityDescriptorError(`${path} must be an object`);
  const object = value as Record<string, unknown>;
  for (const member of Object.keys(object)) {
    if (!Object.hasOwn(shape, member)) throw new IdentityDescriptorError(`unknown member ${path}.${member}`);
  }
  for (const [member, inner] of Object.entries(shape)) check(object[member], inner, `${path}.${member}`);
}

/**
 * Reads an identity descriptor, from its JSON text or its value. It refuses
 * any version but 1, unknown members, an empty name, a role table without
 * its grant table or the other way round, and a name holding a NUL, which
 * no quoting can carry.
 */
export function parseIdentityDescriptor(input: string | unknown): IdentityDescriptor {
  let value: unknown = input;
  if (typeof input === 'string') {
    try {
      value = JSON.parse(input);
    } catch (error) {
      throw new IdentityDescriptorError(`not JSON: ${(error as Error).message}`);
    }
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new IdentityDescriptorError('must be a JSON object');
  const d = value as Record<string, unknown>;
  for (const member of Object.keys(d)) {
    if (member !== 'version' && !Object.hasOwn(SHAPE, member)) throw new IdentityDescriptorError(`unknown member ${member}`);
  }
  if (d.version !== DESCRIPTOR_VERSION) throw new IdentityDescriptorError(`version ${JSON.stringify(d.version)}, want ${DESCRIPTOR_VERSION}`);
  const hasRole = d.role !== undefined && d.role !== null;
  const hasGrant = d.roleGrant !== undefined && d.roleGrant !== null;
  if (hasRole !== hasGrant) throw new IdentityDescriptorError('role and roleGrant are present together or not at all');
  for (const member of ['user', 'session', 'credential', ...(hasRole ? ['role', 'roleGrant'] : [])]) {
    check(d[member], SHAPE[member]!, member);
  }
  const { role, roleGrant, ...rest } = d;
  return (hasRole ? { ...rest, role, roleGrant } : rest) as unknown as IdentityDescriptor;
}

/** Quotes an identifier as every dialect a store speaks reads it: the name in double quotes, each double quote doubled. */
export function quoteIdentifier(name: string): string {
  return `"${name.replaceAll('"', '""')}"`;
}
