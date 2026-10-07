package main

import (
	"os"

	"github.com/egginsect/codvps/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		cli.ReportError(os.Stderr, err)
		if exitErr, ok := err.(*cli.ExitError); ok {
			os.Exit(exitErr.Code)
		}
		if _, ok := err.(*cli.NotImplementedError); ok {
			os.Exit(2)
		}
		// A command may report a soft, non-implementation failure with its
		// own documented exit code (see the exit-code contract in
		// docs/design/go-core.md).
		if ec, ok := err.(interface{ ExitCode() int }); ok {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}
