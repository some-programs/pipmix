// pipmix is a terminal mixer for PipeWire.
package main

import (
	"fmt"
	"os"

	"github.com/some-programs/pipmix/internal/pipmix"
)

func main() {
	if err := pipmix.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pipmix: %v\n", err)
		os.Exit(1)
	}
}
