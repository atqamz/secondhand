package cmd

import (
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/selfupdate"
)

func TestBuildInfoIsFleetIndependentAndStructured(t *testing.T) {
	root := newRootCmd(selfupdate.BuildInfo{
		Version:      "1.2.3",
		Channel:      selfupdate.ChannelStable,
		Commit:       "0123456789abcdef0123456789abcdef01234567",
		Distribution: selfupdate.DistributionGitHub,
	})
	root.SetArgs([]string{"build-info"})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(new(strings.Builder))

	if _, err := root.ExecuteC(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"version: 1.2.3\n",
		"channel: stable\n",
		"commit: 0123456789abcdef0123456789abcdef01234567\n",
		"distribution: github\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("build-info output = %q, want %q", out.String(), want)
		}
	}
}
