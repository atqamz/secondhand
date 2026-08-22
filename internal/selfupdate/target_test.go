package selfupdate

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/faketool"
)

const edgeTestCommit = "0123456789abcdef0123456789abcdef01234567"
const stableTestCommit = "fedcba9876543210fedcba9876543210fedcba98"

func writeFakeGHTarget(t *testing.T, stable, edge string) {
	t.Helper()
	faketool.GH{Release: faketool.GHRelease{Tag: stable, Commit: stableTestCommit, EdgeCommit: edge}}.Install(t, faketool.Bin(t))
}

func TestNormalizeBuildInfoDefaultsUnknownChannelsToDev(t *testing.T) {
	for _, channel := range []string{"", "nightly", "EDGE"} {
		info := NormalizeBuildInfo("edge.deadbeef", channel, edgeTestCommit, DistributionGitHub)
		if info.Channel != ChannelDev {
			t.Errorf("NormalizeBuildInfo(%q) channel = %q, want %q", channel, info.Channel, ChannelDev)
		}
	}

	info := NormalizeBuildInfo("edge.deadbeef", ChannelEdge, edgeTestCommit, DistributionGitHub)
	if info != (BuildInfo{Version: "edge.deadbeef", Channel: ChannelEdge, Commit: edgeTestCommit, Distribution: DistributionGitHub}) {
		t.Fatalf("NormalizeBuildInfo(edge) = %#v", info)
	}
}

func TestNormalizeBuildInfoDetectsDistributionWhenUnset(t *testing.T) {
	t.Cleanup(func() { readBuildInfo = debug.ReadBuildInfo })

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.5.0"}}, true
	}
	if info := NormalizeBuildInfo("v0.5.0", ChannelStable, "", ""); info.Distribution != DistributionGo {
		t.Fatalf("distribution = %q, want %q for a go-install build", info.Distribution, DistributionGo)
	}

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}
	if info := NormalizeBuildInfo("dev", ChannelDev, "", ""); info.Distribution != DistributionSource {
		t.Fatalf("distribution = %q, want %q for a source build", info.Distribution, DistributionSource)
	}
}

func TestResolveTargetStableUsesLatestRelease(t *testing.T) {
	writeFakeGHTarget(t, "v0.5.0", edgeTestCommit)

	target, err := ResolveTarget("atqamz/hand", ChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	want := Target{Channel: ChannelStable, Tag: "v0.5.0", Version: "v0.5.0", Commit: stableTestCommit}
	if target != want {
		t.Fatalf("target = %#v, want %#v", target, want)
	}
}

func TestResolveTargetEdgeUsesFullRefCommit(t *testing.T) {
	writeFakeGHTarget(t, "v0.5.0", edgeTestCommit)

	target, err := ResolveTarget("atqamz/hand", ChannelEdge)
	if err != nil {
		t.Fatal(err)
	}
	want := Target{Channel: ChannelEdge, Tag: "edge", Version: "edge." + edgeTestCommit[:12], Commit: edgeTestCommit}
	if target != want {
		t.Fatalf("target = %#v, want %#v", target, want)
	}
}

// The notice cache remembers a version and a commit, never the tag, so rebuilding
// a target from it has to land on exactly what resolving it would have produced.
func TestCachedTargetMatchesTheResolvedTargetPerChannel(t *testing.T) {
	writeFakeGHTarget(t, "v0.5.0", edgeTestCommit)

	for _, channel := range []string{ChannelStable, ChannelEdge} {
		resolved, err := ResolveTarget("atqamz/hand", channel)
		if err != nil {
			t.Fatal(err)
		}
		if got := cachedTarget(channel, resolved.Version, resolved.Commit); got != resolved {
			t.Fatalf("cached %s target = %#v, want the resolved %#v", channel, got, resolved)
		}
	}
}

func TestResolveTargetRejectsMalformedEdgeCommit(t *testing.T) {
	writeFakeGHTarget(t, "v0.5.0", "not-a-commit")

	if _, err := ResolveTarget("atqamz/hand", ChannelEdge); err == nil || !strings.Contains(err.Error(), "invalid edge commit") {
		t.Fatalf("ResolveTarget malformed edge = %v, want invalid edge commit error", err)
	}
}

func TestResolveTargetRejectsUnknownChannel(t *testing.T) {
	if _, err := ResolveTarget("atqamz/hand", "nightly"); err == nil || !strings.Contains(err.Error(), "invalid release channel") {
		t.Fatalf("ResolveTarget unknown channel = %v, want invalid release channel error", err)
	}
}

func TestNeedsUpdateUsesChannelSpecificIdentity(t *testing.T) {
	cases := []struct {
		name    string
		current BuildInfo
		target  Target
		want    bool
	}{
		{
			name:    "stable newer semver",
			current: BuildInfo{Version: "v0.3.0", Channel: ChannelStable},
			target:  Target{Channel: ChannelStable, Version: "v0.4.0"},
			want:    true,
		},
		{
			name:    "stable same semver",
			current: BuildInfo{Version: "v0.4.0", Channel: ChannelStable},
			target:  Target{Channel: ChannelStable, Version: "v0.4.0"},
			want:    false,
		},
		{
			name:    "edge newer commit",
			current: BuildInfo{Version: "edge.aaaaaaaaaaaa", Channel: ChannelEdge, Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			target:  Target{Channel: ChannelEdge, Commit: edgeTestCommit},
			want:    true,
		},
		{
			name:    "edge same commit",
			current: BuildInfo{Version: "edge.0123456789ab", Channel: ChannelEdge, Commit: edgeTestCommit},
			target:  Target{Channel: ChannelEdge, Commit: edgeTestCommit},
			want:    false,
		},
		{
			name:    "stable to explicit edge",
			current: BuildInfo{Version: "v0.4.0", Channel: ChannelStable},
			target:  Target{Channel: ChannelEdge, Commit: edgeTestCommit},
			want:    true,
		},
		{
			name:    "edge to explicit stable",
			current: BuildInfo{Version: "edge.0123456789ab", Channel: ChannelEdge, Commit: edgeTestCommit},
			target:  Target{Channel: ChannelStable, Version: "v0.4.0"},
			want:    true,
		},
		{
			name:    "dev to stable",
			current: BuildInfo{Version: "dev", Channel: ChannelDev},
			target:  Target{Channel: ChannelStable, Version: "v0.4.0"},
			want:    true,
		},
		{
			name:    "edge missing current commit",
			current: BuildInfo{Version: "edge.0123456789ab", Channel: ChannelEdge},
			target:  Target{Channel: ChannelEdge, Commit: edgeTestCommit},
			want:    true,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := NeedsUpdate(test.current, test.target)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("NeedsUpdate(%#v, %#v) = %t, want %t", test.current, test.target, got, test.want)
			}
		})
	}
}

func TestNeedsUpdateRejectsInvalidTargetChannel(t *testing.T) {
	if _, err := NeedsUpdate(BuildInfo{Version: "v0.1.0", Channel: ChannelStable}, Target{Channel: "nightly", Version: "nightly"}); err == nil {
		t.Fatal("want invalid target channel error")
	}
}

func TestCompareVersionsUsesStableReleaseIdentity(t *testing.T) {
	for _, test := range []struct {
		current string
		target  string
		want    VersionRelation
	}{
		{current: "v1.0.0", target: "1.2.0", want: VersionOlder},
		{current: "1.2.0", target: "v1.2.0", want: VersionSame},
		{current: "v2.0.0", target: "v1.2.0", want: VersionNewer},
	} {
		t.Run(test.current+"-"+test.target, func(t *testing.T) {
			got, err := CompareVersions(test.current, test.target)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("CompareVersions(%q, %q) = %q, want %q", test.current, test.target, got, test.want)
			}
		})
	}
}
