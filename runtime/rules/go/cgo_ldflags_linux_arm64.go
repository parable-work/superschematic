//go:build linux && arm64

package rules

// #cgo LDFLAGS: -L${SRCDIR}/lib/linux_arm64 -lsuperschematic_rules -lm -ldl -lpthread
import "C"
