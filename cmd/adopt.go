package cmd

import (
	"fmt"

	"github.com/atqamz/hand/internal/axi"
	"github.com/atqamz/hand/internal/selfupdate"
	"github.com/spf13/cobra"
)

func newAdoptCmd() *cobra.Command {
	var source string
	var target string
	var version string
	var commit string

	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Select a verified direct GitHub build at its install path",
		Args:  usageArgs(cobra.NoArgs),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			for name, value := range map[string]string{"source": source, "target": target, "version": version, "commit": commit} {
				if value == "" {
					return usageValue(true, fmt.Errorf("--%s is required", name))
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			want := selfupdate.BuildInfo{
				Version:      version,
				Channel:      selfupdate.ChannelStable,
				Commit:       commit,
				Distribution: selfupdate.DistributionGitHub,
			}
			result, err := selfupdate.Adopt(cmd.Context(), source, target, want)
			if err != nil {
				return err
			}
			var doc axi.Doc
			doc.Field("result", result.Result)
			doc.Field("path", result.Path)
			doc.Field("version", want.Version)
			doc.Field("channel", want.Channel)
			doc.Field("commit", want.Commit)
			doc.Field("distribution", want.Distribution)
			return doc.Render(cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "absolute path to the verified staged Hand executable")
	cmd.Flags().StringVar(&target, "target", "", "absolute preferred Hand install path")
	cmd.Flags().StringVar(&version, "version", "", "expected stable release version")
	cmd.Flags().StringVar(&commit, "commit", "", "expected stable release commit")
	return cmd
}
