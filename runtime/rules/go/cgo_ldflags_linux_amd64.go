//go:build linux && amd64

package rules

// #cgo LDFLAGS: -L${SRCDIR}/lib/linux_amd64 -lsuperschematic_rules -lm -ldl -lpthread
import "C"
