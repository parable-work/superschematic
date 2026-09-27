//go:build darwin && amd64

package versiongraph

// #cgo LDFLAGS: -L${SRCDIR}/lib/darwin_amd64 -lsuperschematic_versiongraph
import "C"
