package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/atqamz/hand/internal/faketool"
	"github.com/atqamz/hand/internal/selfupdate"
)

const edgeCommandCommit = "0123456789abcdef0123456789abcdef01234567"
const edgeCommandCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// Mirrors the unexported cache record selfupdate/notice.go reads and writes,
// so a test can seed or inspect state/.version-check without exporting it.
type versionCheckFixture struct {
	CheckedAt time.Time `json:"checked_at"`
	Channel   string    `json:"channel,omitempty"`
	Latest    string    `json:"latest"`
	Commit    string    `json:"commit,omitempty"`
}

func writeVersionCheckFixture(t *testing.T, home string, cache versionCheckFixture) {
	t.Helper()
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", ".version-check"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readVersionCheckFixture(t *testing.T, home string) versionCheckFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "state", ".version-check"))
	if err != nil {
		t.Fatal(err)
	}
	var cache versionCheckFixture
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	return cache
}

func writeFakeGHChannels(t *testing.T, stable, edge string) {
	t.Helper()
	faketool.GH{Release: faketool.GHRelease{Tag: stable, EdgeCommit: edge}}.Install(t, faketool.Bin(t))
}

func writeFakeGHEdgeUpdate(t *testing.T, commit, notes, fixtureDir string) {
	t.Helper()
	faketool.GH{Release: faketool.GHRelease{
		EdgeCommit: commit, EdgeNotes: notes, EdgeDir: fixtureDir,
	}}.Install(t, faketool.Bin(t))
}

func TestUpdateCheckFollowsEmbeddedEdgeChannel(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	writeFakeGHChannels(t, "v0.5.0", edgeCommandCommit)

	cmd := newUpdateCmd(selfupdate.BuildInfo{
		Version:      "edge.aaaaaaaaaaaa",
		Channel:      selfupdate.ChannelEdge,
		Commit:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Distribution: selfupdate.DistributionGitHub,
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"current: edge.aaaaaaaaaaaa\n",
		"current_channel: edge\n",
		"current_commit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
		"latest: edge.0123456789ab\n",
		"latest_channel: edge\n",
		"latest_commit: " + edgeCommandCommit + "\n",
		"update_available: true\n",
		"updated: false\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
}

func TestUpdateCheckExplicitlySwitchesStableToEdge(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	writeFakeGHChannels(t, "v0.5.0", edgeCommandCommit)

	cmd := newUpdateCmd(selfupdate.BuildInfo{Version: "v0.9.0", Channel: selfupdate.ChannelStable})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check", "--channel", selfupdate.ChannelEdge})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "latest_channel: edge\n") || !strings.Contains(out.String(), "update_available: true\n") {
		t.Fatalf("output = %q, want explicit edge switch", out.String())
	}
}

func TestUpdateCheckEdgeWithMatchingCommitIsUpToDate(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	writeFakeGHChannels(t, "v0.5.0", edgeCommandCommit)

	cmd := newUpdateCmd(selfupdate.BuildInfo{
		Version: "edge.0123456789ab",
		Channel: selfupdate.ChannelEdge,
		Commit:  edgeCommandCommit,
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "update_available: false\n") {
		t.Fatalf("output = %q, want no edge update", out.String())
	}
}

func TestUpdateInstallsEdgeAssetThroughSharedApplyPath(t *testing.T) {
	execPath := setFakeExecutable(t)
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	fixture := buildUpdateFixture(t, []byte("new edge binary contents"))
	writeFakeGHEdgeUpdate(t, edgeCommandCommit, "edge notes", fixture)

	cmd := newUpdateCmd(selfupdate.BuildInfo{
		Version:      "edge.aaaaaaaaaaaa",
		Channel:      selfupdate.ChannelEdge,
		Commit:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Distribution: selfupdate.DistributionGitHub,
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "latest_channel: edge\n") || !strings.Contains(out.String(), "updated: true\n") {
		t.Fatalf("output = %q, want installed edge update", out.String())
	}
	installed, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != "new edge binary contents" {
		t.Fatalf("installed binary = %q, want edge asset contents", installed)
	}
}

func TestUpdateRejectsInvalidChannelAsUsageError(t *testing.T) {
	cmd := newUpdateCmd(selfupdate.BuildInfo{Version: "v0.1.0", Channel: selfupdate.ChannelStable})
	cmd.SetArgs([]string{"--check", "--channel", "nightly"})
	if code := exitCodeFor(t, cmd.Execute()); code != 2 {
		t.Fatalf("exit code = %d, want usage code 2", code)
	}
}

func TestUpdateCheckReconcilesARolledBackEdgeCache(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeVersionCheckFixture(t, home, versionCheckFixture{
		CheckedAt: time.Now(),
		Channel:   selfupdate.ChannelEdge,
		Latest:    "edge." + edgeCommandCommit[:12],
		Commit:    edgeCommandCommit,
	})
	writeFakeGHChannels(t, "v0.5.0", edgeCommandCommitA)

	cmd := newUpdateCmd(selfupdate.BuildInfo{
		Version: "edge." + edgeCommandCommitA[:12],
		Channel: selfupdate.ChannelEdge,
		Commit:  edgeCommandCommitA,
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "update_available: false\n") {
		t.Fatalf("output = %q, want no update available once the live target rolls back to the installed build", out.String())
	}

	cache := readVersionCheckFixture(t, home)
	if cache.Commit != edgeCommandCommitA {
		t.Fatalf("cache commit = %q, want the live target %q, not the withdrawn build", cache.Commit, edgeCommandCommitA)
	}
}

func TestUpdateCheckReconcilesAForwardEdgeCache(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeVersionCheckFixture(t, home, versionCheckFixture{
		CheckedAt: time.Now(),
		Channel:   selfupdate.ChannelEdge,
		Latest:    "edge." + edgeCommandCommitA[:12],
		Commit:    edgeCommandCommitA,
	})
	writeFakeGHChannels(t, "v0.5.0", edgeCommandCommit)

	cmd := newUpdateCmd(selfupdate.BuildInfo{
		Version: "edge." + edgeCommandCommitA[:12],
		Channel: selfupdate.ChannelEdge,
		Commit:  edgeCommandCommitA,
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "update_available: true\n") {
		t.Fatalf("output = %q, want an available update for the newer live edge build", out.String())
	}

	cache := readVersionCheckFixture(t, home)
	if cache.Commit != edgeCommandCommit {
		t.Fatalf("cache commit = %q, want the newer live target recorded", cache.Commit)
	}
}

func TestUpdateCheckLeavesNoPositiveCacheClaimOnResolutionFailure(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	t.Setenv("PATH", t.TempDir())

	cmd := newUpdateCmd(selfupdate.BuildInfo{Version: "v0.1.0", Channel: selfupdate.ChannelStable})
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want the existing error behavior when gh is unreachable")
	}

	if _, err := os.Stat(filepath.Join(home, "state", ".version-check")); !os.IsNotExist(err) {
		t.Fatalf("got a version cache written after a failed live resolution, err=%v", err)
	}
}

func TestUpdateCheckWarnsWhenTheCacheCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce directory write permissions the same way")
	}
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeGHChannels(t, "v0.5.0", edgeCommandCommit)

	stateDir := filepath.Join(home, "state")
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	cmd := newUpdateCmd(selfupdate.BuildInfo{
		Version: "edge." + edgeCommandCommitA[:12],
		Channel: selfupdate.ChannelEdge,
		Commit:  edgeCommandCommitA,
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got %v, want a successful check despite an unwritable cache", err)
	}
	if !strings.Contains(out.String(), "update_available: true\n") {
		t.Fatalf("output = %q, want the check result unaffected by the cache failure", out.String())
	}
	if !strings.Contains(errOut.String(), "warning: reconcile the version notice cache:") {
		t.Fatalf("got stderr %q, want a reconcile warning", errOut.String())
	}
}

func TestUpdateApplyLeavesNoticeCoherentWithTheInstalledBuild(t *testing.T) {
	setFakeExecutable(t)
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	fixture := buildUpdateFixture(t, []byte("new edge binary contents"))
	writeFakeGHEdgeUpdate(t, edgeCommandCommit, "edge notes", fixture)

	cmd := newUpdateCmd(selfupdate.BuildInfo{
		Version: "edge." + edgeCommandCommitA[:12],
		Channel: selfupdate.ChannelEdge,
		Commit:  edgeCommandCommitA,
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	cache := readVersionCheckFixture(t, home)
	if cache.Channel != selfupdate.ChannelEdge || cache.Commit != edgeCommandCommit {
		t.Fatalf("cache = %+v, want it to record the installed edge build", cache)
	}

	bin := faketool.Bin(t)
	faketool.GH{}.Install(t, bin)
	installed := selfupdate.BuildInfo{Version: "edge." + edgeCommandCommit[:12], Channel: selfupdate.ChannelEdge, Commit: edgeCommandCommit}
	if notice := selfupdate.CheckNoticeForBuild(home, selfupdate.Repo, installed); notice != "" {
		t.Fatalf("got %q, want no banner once notice state matches the installed build", notice)
	}
}
