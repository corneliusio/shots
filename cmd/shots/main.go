// Command shots takes automated screenshots with Chrome.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mostlyserious/shots/internal/command"
)

func main() {
	// Cancelling this context is the single cleanup path: it closes pages,
	// terminates a browser shots launched, and unwinds every command's defers.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(command.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
