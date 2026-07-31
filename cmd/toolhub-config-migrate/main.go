package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Junhao2314/toolhub/internal/configmigration"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	options, err := configmigration.LoadOptions(args)
	if err != nil {
		writePublicError(stderr, err)
		return exitCode(err)
	}
	defer options.ClearKeys()
	report, err := configmigration.Execute(ctx, options)
	if err != nil {
		writePublicError(stderr, err)
		return exitCode(err)
	}
	if _, err := stdout.Write(report.Human()); err != nil {
		writePublicError(stderr, &configmigration.Error{Code: "report_write_failed", Message: "human report could not be written", Cause: err})
		return 1
	}
	if err := report.WriteJSON(options.ReportJSONPath); err != nil {
		writePublicError(stderr, err)
		return 1
	}
	return 0
}

func writePublicError(writer io.Writer, err error) {
	code, message := configmigration.PublicError(err)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func exitCode(err error) int {
	code, _ := configmigration.PublicError(err)
	if code == "invalid_options" {
		return 2
	}
	return 1
}
