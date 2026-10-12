// Package shop is the shop's Go module: each API's implementation, a
// package of its own at the path the naming file's [implementation_paths]
// go template gives, go/{service} (go/shop-api and go/shop-orders), and the
// tests, which serve both APIs and call them through their generated SDKs.
//
// No package here authenticates a caller. shop-db's User table has the
// core User trait (D50), so both APIs' generated servers authenticate with
// the identity runtime: shop-api serves the session routes a user signs in
// with and the administration routes staff manage users and roles with,
// and shop-orders accepts the sessions those sign-ins start. The servers
// the stack runs build the identity service over shop-db themselves.
package shop
