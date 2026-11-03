//go:build darwin && arm64

package rules

// #cgo LDFLAGS: -L${SRCDIR}/lib/darwin_arm64 -lsuperschematic_rules
import "C"
