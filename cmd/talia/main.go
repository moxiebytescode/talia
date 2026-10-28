package main

import (
	"os"

	"github.com/moxiebytescode/talia"
)

var exitFunc = os.Exit

func main() {
	exitFunc(talia.RunCLI(os.Args[1:]))
}
