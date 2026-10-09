// Command umpire is the umpire command line tool.
package main

import (
	"context"
	"os"

	"github.com/nnutter/umpire/internal/cli"
)

func main() {
	if err := cli.Execute(context.Background(), os.Args[1:], ".", os.Stdin, os.Stdout, os.Stderr, Version); err != nil {
		os.Exit(1)
	}
}
