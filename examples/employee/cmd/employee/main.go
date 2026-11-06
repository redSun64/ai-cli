package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"

	aicli "github.com/redSun64/ai-cli"
	"github.com/redSun64/ai-cli/examples/employee"
)

func main() {
	app, err := employee.New(employee.Options{})
	if err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"ok": false, "error": err})
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := app.RunCLI(ctx, os.Args[1:], os.Stdout, os.Stderr, aicli.CLIOptions{})
	stop()
	os.Exit(code)
}
