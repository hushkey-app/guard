//go:build js && wasm

package lifecycle

import "syscall/js"

func Mount(page string) {
	if fn := js.Global().Get("guardPageMount"); fn.Type() == js.TypeFunction {
		fn.Invoke(page)
	}
}

func Unmount(page string) {
	if fn := js.Global().Get("guardPageUnmount"); fn.Type() == js.TypeFunction {
		fn.Invoke(page)
	}
}
