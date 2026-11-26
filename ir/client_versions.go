package ir

// The names of an API's client versions (D143). An API whose config says
// `clientVersions: true` reads MinClientVersionsSetting, an optional
// setting of its EnvConfig that lists the oldest version of each client it
// serves (`cli=0.5.0,desktop=2.1.0`), and answers a request whose
// ClientVersionHeader names a listed client at a lower version with 426
// and the problem ClientTooOldCode. A generated SDK sends the header when
// its caller names its client. The Go, TypeScript and Rust HTTP runtimes
// read the setting and decide each request alike
// (runtime/http/testdata/client_version_parity.json).
const (
	// MinClientVersionsSetting is the EnvConfig setting, the same for
	// every API a server serves: a client is the application's, not one
	// API's.
	MinClientVersionsSetting = "MIN_CLIENT_VERSIONS"
	// ClientVersionHeader is the request header, `<name>/<version>`.
	ClientVersionHeader = "X-Client-Version"
	// ClientTooOldCode is the code of the 426's problem.
	ClientTooOldCode = "client_too_old"
)
