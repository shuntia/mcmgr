// Command mcmgr is a fast, cross-platform Minecraft server and mod manager.
package main

import (
	"errors"
	"fmt"
	"os"

	"mcmgr/internal/cli"
	"mcmgr/internal/ui"
)

func main() {
	err := cli.Main(os.Args[1:])
	if err == nil {
		return
	}
	if errors.Is(err, cli.ErrUsage) {
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "%serror:%s %v\n", ui.Red, ui.Reset, err)
	os.Exit(1)
}
