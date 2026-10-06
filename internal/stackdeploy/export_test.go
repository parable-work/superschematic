package stackdeploy

import ir "github.com/parable-work/superschematic/ir"

// SetCredentialsOf replaces CredentialsOf for a test and returns what puts
// it back.
func SetCredentialsOf(f func(*ir.ResolvedEnvironment) []Credential) (restore func()) {
	prev := credentialsOf
	credentialsOf = f
	return func() { credentialsOf = prev }
}
