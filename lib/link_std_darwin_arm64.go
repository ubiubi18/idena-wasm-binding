//go:build darwin && arm64

package lib

// #cgo LDFLAGS: -L. -lidena_wasm_darwin_arm64
import "C"
