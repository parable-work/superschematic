// Package identity is the Go runtime of the core user model (D50): it signs
// users in and out, resolves a request's session to a principal, and
// manages users, roles and grants over the tables the core owns. A
// generated Go server wires it in and adds no logic; the TypeScript and
// Rust runtimes do what it does, held to the same vectors
// (runtime/http/testdata/identity_parity.json, which TestWriteParityVectors
// writes with -update).
//
// The pieces:
//
//   - Config, the JSON every runtime reads alike: the session's lifetime,
//     idle timeout and touch interval, the cookie, the trusted origins and
//     the password hash's cost. ParseConfig reads it strictly.
//   - Passwords: argon2id as PHC strings (HashPassword, VerifyPassword,
//     which also says when a hash should be written again at the
//     config's cost), and Auth.Password's rule (CheckPassword).
//   - Tokens: 32 random bytes in base64url (NewToken), kept as their
//     SHA-256 (HashToken).
//   - The credential on a request (ExtractCredential): Authorization:
//     Bearer first, the session cookie only without an Authorization
//     header, and an Authorization that is not a usable bearer token
//     refused, with no fallback to the cookie.
//   - The session cookie (Config.SessionCookie, Config.ClearCookie).
//   - The cross-origin check on a cookie request and a cookie login,
//     net/http's CrossOriginProtection with the trusted origins, and the
//     credentialed CORS middleware for them (CORS).
//   - Roles: the permission form (ValidPermission), a principal's
//     permissions (EffectivePermissions), the rule that no one grants what
//     they do not hold (Uncovered), and capabilities over the route
//     requirements a server passes (Route, CapabilitiesOf).
//   - Store, and SQLStore, its database/sql implementation for Postgres
//     and SQLite, built from the schema's identity descriptor.
//   - Service, the operations over a Store, a Config and a clock, and its
//     net/http handlers (Service.Handler, Service.Routes) and middleware
//     (Service.Middleware), which puts the principal on the context
//     through the session package's helpers, so RequireAuth and
//     RequirePermissions work unchanged.
//
// The package is provider-neutral (D6) and links no database driver.
package identity
