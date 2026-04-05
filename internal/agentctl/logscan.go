package agentctl

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/noderax/noderax-agent/internal/logscan"
)

func (c CLI) LogScan(ctx context.Context, args []string) error {
	flagSet := flag.NewFlagSet("log-scan", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	requestPath := flagSet.String("request", "", "")
	if err := flagSet.Parse(args); err != nil {
		return err
	}
	if *requestPath == "" {
		return fmt.Errorf("log-scan requires --request")
	}
	if flagSet.NArg() > 0 {
		return fmt.Errorf("unexpected log-scan arguments: %v", flagSet.Args())
	}

	request, err := logscan.LoadRequest(*requestPath)
	if err != nil {
		return err
	}
	_ = os.Remove(*requestPath)

	result, err := logscan.Run(ctx, request)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(c.stdoutOrDefault())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}
