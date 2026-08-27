// Command tokentelemetry reports local token usage and cost across AI coding
// agents. It reads only files already on disk and makes no network calls.
package main

import (
	"os"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args)) }
