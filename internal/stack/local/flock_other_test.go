//go:build !unix

package local_test

import (
	"errors"
	"os"
)

// flock takes no lock where flock is missing.
func flock(*os.File) error { return errors.New("flock is unix's") }
