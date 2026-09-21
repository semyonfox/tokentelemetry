// Command tt is the short name for tokentelemetry.
package main

import (
	"os"

	"github.com/semyonfox/tokentelemetry/engine/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args)) }
