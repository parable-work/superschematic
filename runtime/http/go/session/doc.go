// Package session is the generic authentication model of the http-runtime:
// a bearer session resolved to a principal, the principal's roles with
// plain-string permissions, and the request-context and middleware helpers
// generated APIs built with the core "session" auth provider import. It knows
// nothing about tenants, typed scalars or any one project's permission
// vocabulary; a project with its own identity and permission types builds
// them on top of it, the way the authmw, authz and requestctx packages next
// to it do (docs/extension-model.md section 8.2).
//
// Permissions are dotted paths. A granted permission covers a required one
// when they are equal or the required one is nested under it ("a.b" is
// covered by "a"). There is no root permission that covers everything; a
// project that wants one plugs its own matcher into RequirePermissionsWith
// or builds its middleware on Guard.
package session
