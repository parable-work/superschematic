//go:build !(linux && (amd64 || arm64))

package tasks

func sandbox() error { return ErrSandboxUnsupported }
