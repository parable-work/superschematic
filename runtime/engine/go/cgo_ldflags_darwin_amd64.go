//go:build darwin && amd64

package engine

// #cgo LDFLAGS: -L${SRCDIR}/lib/darwin_amd64 -lsuperschematic_engine -framework SystemConfiguration -framework CoreFoundation -liconv -lm
import "C"
