//go:build linux && amd64

package engine

// #cgo LDFLAGS: -L${SRCDIR}/lib/linux_amd64 -lsuperschematic_engine -lm -ldl -lpthread
import "C"
