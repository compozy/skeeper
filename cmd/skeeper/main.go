// Package main is the skeeper CLI entrypoint.
package main

import (
	"context"
	"io"
	"os"

	"github.com/compozy/skeeper/internal/cli"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return cli.Execute(ctx, args, stdout, stderr)
}
