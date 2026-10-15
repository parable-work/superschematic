//go:build linux && arm64

package engine

// #cgo LDFLAGS: -L${SRCDIR}/lib/linux_arm64 -lsuperschematic_engine -lm -ldl -lpthread
import "C"
