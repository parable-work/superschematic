//go:build linux && arm64

package versiongraph

// #cgo LDFLAGS: -L${SRCDIR}/lib/linux_arm64 -lsuperschematic_versiongraph -lm -ldl -lpthread
import "C"
