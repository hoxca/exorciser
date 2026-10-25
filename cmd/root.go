// Package cmd implements the exorciser command line.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Version and BuildDate are injected by the Makefile through -ldflags.
var (
	Version   = "v0"
	BuildDate = "unknown"
)

var verbose bool

// Execute runs the command line and reports whether it succeeded.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "exorciser: %v\n", err)
		return err
	}
	return nil
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "exorciser",
		Short: "Detect diffuse ghost artefacts in astronomical images",
		Long: `exorciser detects diffuse ghost artefacts in monochrome astronomical
frames in XISF format.

detect compares every frame with a ghost-free reference frame and reports
whether the strongest feature in a window is significant.

scan builds a ghost master and a clean master from a detection report, subtracts
them, and catalogues every diffuse artefact in the difference.

annotate draws the catalogue on an RGB copy of the ghost master, with a red
cross at every artefact position.

union merges the per-filter catalogues, averaging each artefact's jittered
position, and marks in yellow the union entries a filter is missing.

erase inpaints the catalogued artefacts in the frames that show them, using the
clean master as reference and preserving each frame's sky level and noise.

Frames are analysed after star alignment, so positions are in registered pixels
and move with each frame's alignment offset.`,
		Version:       fmt.Sprintf("%s (built %s)", Version, BuildDate),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			setupLogging()
		},
	}
	root.SetVersionTemplate("exorciser {{.Version}}\n")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "log debugging detail")

	root.AddCommand(newDetectCmd())
	root.AddCommand(newScanCmd())
	root.AddCommand(newAnnotateCmd())
	root.AddCommand(newUnionCmd())
	root.AddCommand(newEraseCmd())
	return root
}

func setupLogging() {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}
