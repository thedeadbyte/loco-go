// Command loco is an agentic coding CLI for local open-weight models via Ollama.
package main

import (
	"os"

	"github.com/thedeadbyte/loco-go/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
