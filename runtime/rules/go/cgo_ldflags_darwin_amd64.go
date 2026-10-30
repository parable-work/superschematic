//go:build darwin && amd64

package rules

// #cgo LDFLAGS: -L${SRCDIR}/lib/darwin_amd64 -lsuperschematic_rules
import "C"
