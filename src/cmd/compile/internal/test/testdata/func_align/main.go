package main

//go:noinline
func foo() {
	return
}

//go:noinline
func bar() {
	return
}

//go:noinline
func baz() {
	return
}

//go:noescape
func asm_foo()

//go:noescape
func asm_bar()

//go:noescape
func asm_baz()

func main() {
	foo()
	bar()
	baz()
	asm_foo()
	asm_bar()
	asm_baz()
}
