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
		Long: `exorciser compares monochrome astronomical frames in XISF format with a
ghost-free reference frame. After normalising and removing a local background,
it matched-filters the residual in a square window and reports whether a
diffuse artefact is present, how significant it is, and where its centre lies.

Frames are analysed after star alignment, so the reported peak position is in
registered pixels and moves with each frame's alignment offset.`,
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
