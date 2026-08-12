//go:build !(js && wasm)

package lifecycle

func Mount(string)   {}
func Unmount(string) {}
