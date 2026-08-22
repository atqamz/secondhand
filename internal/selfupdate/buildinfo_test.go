package selfupdate

import (
	"context"
	"strings"
	"testing"
)

func TestParseBuildInfoOutputRequiresTheFourIdentityFields(t *testing.T) {
	got, err := parseBuildInfoOutput(strings.NewReader("version: 1.2.3\nchannel: stable\ncommit: abc\ndistribution: github\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: "abc", Distribution: DistributionGitHub}
	if got != want {
		t.Fatalf("build info = %#v, want %#v", got, want)
	}

	for _, output := range []string{
		"version: 1.2.3\nchannel: stable\ncommit: abc\n",
		"version: 1.2.3\nchannel: stable\ncommit: abc\ndistribution: github\ndistribution: source\n",
	} {
		if _, err := parseBuildInfoOutput(strings.NewReader(output)); err == nil {
			t.Fatalf("parseBuildInfoOutput(%q) succeeded, want an identity error", output)
		}
	}
}

func TestSameBuildComparesTheCompleteIdentity(t *testing.T) {
	base := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: "abc", Distribution: DistributionGitHub}
	for name, other := range map[string]BuildInfo{
		"same":         base,
		"version":      {Version: "1.2.4", Channel: ChannelStable, Commit: "abc", Distribution: DistributionGitHub},
		"channel":      {Version: "1.2.3", Channel: ChannelEdge, Commit: "abc", Distribution: DistributionGitHub},
		"commit":       {Version: "1.2.3", Channel: ChannelStable, Commit: "def", Distribution: DistributionGitHub},
		"distribution": {Version: "1.2.3", Channel: ChannelStable, Commit: "abc", Distribution: DistributionSource},
	} {
		want := name == "same"
		if got := SameBuild(base, other); got != want {
			t.Errorf("SameBuild(%s) = %t, want %t", name, got, want)
		}
	}
}

func TestReadExecutableBuildInfoRunsAnAbsoluteBinary(t *testing.T) {
	if _, err := ReadExecutableBuildInfo(context.Background(), "/path/that/does/not/exist"); err == nil {
		t.Fatal("ReadExecutableBuildInfo succeeded for a missing executable")
	}
}
