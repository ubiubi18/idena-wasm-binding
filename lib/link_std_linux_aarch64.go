//go:build linux && arm64

package lib

// #cgo LDFLAGS: -L. -lidena_wasm_linux_aarch64 -lgcc_s -lutil -lrt -lpthread -lm -ldl -lc
import "C"
