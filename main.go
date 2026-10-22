// Command exorciser looks for artefacts in astronomical images.
package main

import (
	"os"

	"github.com/hoxca/exorciser/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
