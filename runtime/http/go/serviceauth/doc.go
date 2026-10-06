// Package serviceauth is the service step of a generated Go server: it
// establishes which deployable is calling, beside the end user the auth
// provider establishes, and admits a route's service clause (D37,
// docs/stack-model.md section 9).
//
// A service credential travels in "Service-Authorization: Bearer <jwt>". An
// Authenticator reads only that header, never Authorization, and returns no
// caller when the header is absent. Verifier is the standard Authenticator:
// a JWT verifier over the issuers, keys, audiences and caller identities of
// a Config, with no cloud library (D6). It verifies RS256 (Google ID tokens,
// Kubernetes service account tokens), ES256 and EdDSA (the generic
// connector's key-pair tokens, whose alg the callee also accepts as
// Ed25519). A credential that does not verify is 401 service_unauthorized,
// a verified identity the config does not list is 403 service_forbidden,
// and keys that cannot be fetched with none cached is 503
// service_unavailable, each an *Error that WriteError renders as an RFC 9457
// problem with a code member.
//
// The gate is three middlewares a generated route chain puts after the rate
// limit and the body limit:
//
//	Authenticate(cfg.ServiceAuthenticator) // every route: verify when present
//	Require("shop-orders")                 // @requireService: a listed caller, then the user step if any
//	AllowOr([]string{"shop-orders"},       // @allowService: a listed caller skips the user step
//		cfg.AuthMiddleware, runtimesession.RequirePermissions("stock.write"))
//
// A route with a service clause runs its end-user step after the service
// step, inside its own chain: after Require, or as AllowOr's user
// middlewares. A route without one keeps the protected group's
// AuthMiddleware and gains Authenticate after its body limit, so a caller
// is still verified and put on the request there.
//
// Delegation forwards the end user's token per call through the call's
// context: Authenticate (or CaptureAuthorization alone) stores the
// request's Authorization bearer on the context, and ForwardedToken, which
// has the Go SDK's TokenProvider shape, reads it back for a client's
// GetToken hook.
//
// The client side is a TokenSource per platform: GoogleIDToken (Cloud Run's
// metadata server), TokenFile (a Kubernetes projected token) and
// SignedToken (an edge's Ed25519 key). Each caches its token and is safe
// for concurrent use.
//
// runtime/http/testdata/serviceauth_parity.json holds the vectors the Go,
// TypeScript and Rust runtimes all run through their gates; this package's
// TestWriteParityVectors writes it with -update.
package serviceauth
