//go:build darwin && arm64

package engine

// #cgo LDFLAGS: -L${SRCDIR}/lib/darwin_arm64 -lsuperschematic_engine -framework SystemConfiguration -framework CoreFoundation -liconv -lm
import "C"
