package main

import (
	"fmt"
	"os"

	"github.com/flanksource/commons-db/cmd/query/internal/commands"
)

func main() {
	root, err := commands.New(commands.Options{Args: os.Args[1:], Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
