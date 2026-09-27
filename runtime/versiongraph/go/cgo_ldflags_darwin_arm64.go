//go:build darwin && arm64

package versiongraph

// #cgo LDFLAGS: -L${SRCDIR}/lib/darwin_arm64 -lsuperschematic_versiongraph
import "C"
