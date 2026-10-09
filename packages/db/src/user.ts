/**
 * The User trait's configuration. `login` names the field a user signs in
 * with: a @unique field typed by a case-insensitive scalar, such as
 * Contact.Email or Identity.Slug. `name` names the field a user's display
 * name comes from, a string or a scalar whose values are strings; the login
 * when absent.
 */
export interface UserConfig {
  readonly login: string;
  readonly name?: string;
}

/**
 * Marks the DB table whose rows are the project's users, the core user
 * model's User trait: `implements User<{ login: "email" }>`, with `name`
 * beside `login` when the display name is another field. A DB schema has at
 * most one. superschematic adds the Session and UserCredential tables beside
 * it.
 *
 * Like Trait<T>, User resolves to an empty object type, so the compiler
 * imposes no members; superschematic reads the configuration from the type
 * argument, a type literal of string literals. A class implements User beside
 * other traits and still extends a base class.
 */
export type User<Config extends UserConfig> = NonNullable<unknown>;

/**
 * Marks the DB table whose rows are roles, the core user model's UserRole
 * trait: `implements UserRole`. The table needs a User table in the same
 * schema, a @unique text `name` and a `permissions` list of strings, and a
 * schema has at most one. superschematic adds the UserRoleGrant table that
 * grants each user their roles. Like User, it resolves to an empty object
 * type and takes no type argument.
 */
export type UserRole = NonNullable<unknown>;
