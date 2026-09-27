//go:build linux && amd64

package versiongraph

// #cgo LDFLAGS: -L${SRCDIR}/lib/linux_amd64 -lsuperschematic_versiongraph -lm -ldl -lpthread
import "C"
