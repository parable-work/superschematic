package cigen

import "testing"

// SetReleaseVersion makes the generator render as release version, "" for
// a checkout build, until the test ends.
func SetReleaseVersion(t testing.TB, version string) {
	t.Helper()
	old := releaseVersion
	releaseVersion = func() string { return version }
	t.Cleanup(func() { releaseVersion = old })
}
