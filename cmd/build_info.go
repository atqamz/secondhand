package cmd

import (
	"github.com/atqamz/hand/internal/axi"
	"github.com/atqamz/hand/internal/selfupdate"
	"github.com/spf13/cobra"
)

func newBuildInfoCmd(info selfupdate.BuildInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build-info",
		Short: "Show the Fleet-independent identity of this Hand binary",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			var doc axi.Doc
			doc.Field("version", info.Version)
			doc.Field("channel", info.Channel)
			doc.Field("commit", info.Commit)
			doc.Field("distribution", info.Distribution)
			return doc.Render(cmd.OutOrStdout())
		},
	}
	return cmd
}
