package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/faketool"
	"github.com/atqamz/hand/internal/selfupdate"
)

// Fakes "release view --jq" as real gh's --jq flattens its JSON to the raw field value on stdout
// (selfupdate.go's runGH callers use --jq .tagName/.body), so a plain string with exit 0 is the faithful
// shape - no envelope to reproduce here, unlike herdr's call()/callVoid().
func writeFakeGHReleaseView(t *testing.T, tag string) {
	t.Helper()
	bin := faketool.Bin(t)
	faketool.GH{Release: faketool.GHRelease{Tag: tag}}.Install(t, bin)
}

// Fakes the three gh invocations a full `hand update` makes: the tag lookup and the release notes lookup,
// both "release view --jq" in the shape writeFakeGHReleaseView documents above, plus the asset download.
func writeFakeGHUpdate(t *testing.T, tag, notes, fixtureDir string) {
	t.Helper()
	bin := faketool.Bin(t)
	faketool.GH{Release: faketool.GHRelease{
		Tag: tag, Notes: notes, FixtureDir: fixtureDir,
	}}.Install(t, bin)
}

func buildUpdateFixture(t *testing.T, binaryContent []byte) string {
	t.Helper()
	dir := t.TempDir()
	assetName := selfupdate.AssetName()
	archivePath := filepath.Join(dir, assetName)
	if runtime.GOOS == "windows" {
		file, err := os.Create(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(file)
		header := &zip.FileHeader{Name: "hand.exe", Method: zip.Deflate}
		header.SetMode(0o755)
		entry, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(binaryContent); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		var tarBuf bytes.Buffer
		gz := gzip.NewWriter(&tarBuf)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: "hand", Mode: 0o755, Size: int64(len(binaryContent))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(binaryContent); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, tarBuf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archiveBytes)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The bytes of the shared fake-tool dispatcher, staged as the "new binary" a release fixture
// installs. Real once execDir/hand carries both these bytes and a sibling fake config: os.Args[0]
// resolves to the same path whichever content selfupdate.Apply staged there.
func newBinaryFixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(faketool.DispatcherBinaryPath(t))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setFakeExecutable(t *testing.T) string {
	t.Helper()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hand")
	if err := os.WriteFile(execPath, []byte("old binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := selfupdate.ExecutableOverride
	selfupdate.ExecutableOverride = func() (string, error) { return execPath, nil }
	t.Cleanup(func() { selfupdate.ExecutableOverride = restore })
	return execPath
}

// Stages a fake "new hand" at execDir: once selfupdate.Apply replaces execPath with the same
// dispatcher bytes (newBinaryFixtureBytes), invoking it as `init <fleetHome>` writes agentsMD at
// that path, proving the handoff ran the new binary with the right argv without a real hand init.
func writeFakeNewBinaryInitBehavior(t *testing.T, execDir, agentsMD string) {
	t.Helper()
	faketool.Command{
		Name: "hand",
		Args: true,
		FileAction: &faketool.FileAction{
			PathArg:  1,
			Relative: "AGENTS.md",
			Content:  agentsMD,
		},
	}.Install(t, execDir)
}

// Same as writeFakeNewBinaryInitBehavior, but the fake new binary exits nonzero instead of
// succeeding, for the failed-handoff path.
func writeFakeNewBinaryInitFailure(t *testing.T, execDir string, exitCode int, stderr string) {
	t.Helper()
	faketool.Command{
		Name:   "hand",
		Args:   true,
		Stderr: stderr,
		Exit:   exitCode,
	}.Install(t, execDir)
}

func writeOwnedSessionHook(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/old/path/hand"},{"type":"command","command":"/usr/bin/custom"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
}

const fakeCanonicalAgentsMD = "## Secondhand supervisor bootstrap\n\nBefore responding or acting in a supervising session, run `hand session start`.\n"

func TestUpdateHandsFleetReconciliationToTheNewlyInstalledBinary(t *testing.T) {
	execPath := setFakeExecutable(t)
	execDir := filepath.Dir(execPath)
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeNewBinaryInitBehavior(t, execDir, fakeCanonicalAgentsMD)

	fixture := buildUpdateFixture(t, newBinaryFixtureBytes(t))
	writeFakeGHUpdate(t, "v0.5.0", "fixed the frobnicator", fixture)

	cmd := newUpdateCmd(directStableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "updated: true\n") || !strings.Contains(got, "fleet_reconcile: ok\n") {
		t.Fatalf("got %q, want updated=true and fleet_reconcile=ok", got)
	}
	if !strings.Contains(got, "fixed the frobnicator") {
		t.Fatalf("got %q, want the release notes", got)
	}

	// Proves the new binary ran against this exact fleet home, not merely that some
	// process exited zero: the fake only writes AGENTS.md when invoked as init <home>.
	agentsMD, err := os.ReadFile(filepath.Join(home, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(agentsMD) != fakeCanonicalAgentsMD {
		t.Fatalf("got AGENTS.md %q, want %q", agentsMD, fakeCanonicalAgentsMD)
	}
}

func TestUpdateHandsOffToTheHandHomeRatherThanTheWorkingDirectory(t *testing.T) {
	execPath := setFakeExecutable(t)
	execDir := filepath.Dir(execPath)
	fleetHome := t.TempDir()
	mkFleetDirs(t, fleetHome)
	t.Setenv("HAND_HOME", fleetHome)
	t.Chdir(t.TempDir())
	writeFakeNewBinaryInitBehavior(t, execDir, fakeCanonicalAgentsMD)

	fixture := buildUpdateFixture(t, newBinaryFixtureBytes(t))
	writeFakeGHUpdate(t, "v0.5.0", "fixed the frobnicator", fixture)

	cmd := newUpdateCmd(directStableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fleet_reconcile: ok\n") {
		t.Fatalf("got %q, want fleet_reconcile=ok", out.String())
	}

	if _, err := os.ReadFile(filepath.Join(fleetHome, "AGENTS.md")); err != nil {
		t.Fatalf("got no AGENTS.md written under HAND_HOME: %v", err)
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("got AGENTS.md written under the working directory instead of HAND_HOME, err=%v", err)
	}
}

func TestUpdateSkipsFleetReconciliationOutsideAFleetHome(t *testing.T) {
	setFakeExecutable(t)
	t.Setenv("HAND_HOME", "")
	home := t.TempDir()
	t.Chdir(home)
	// No fake "init" behavior installed: reaching it at all would be the bug this proves absent.

	fixture := buildUpdateFixture(t, newBinaryFixtureBytes(t))
	writeFakeGHUpdate(t, "v0.5.0", "fixed the frobnicator", fixture)

	cmd := newUpdateCmd(directStableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "updated: true\n") || !strings.Contains(got, "fleet_reconcile: no-fleet-home\n") {
		t.Fatalf("got %q, want updated=true and fleet_reconcile=no-fleet-home", got)
	}
	if _, err := os.Stat(filepath.Join(home, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("got AGENTS.md written outside a fleet home, err=%v", err)
	}
}

// "No home here" is the silent skip; "HAND_HOME names something that is not a
// home" is a misconfiguration, and swallowing it would leave the operator with
// an unreconciled fleet home and nothing on stderr saying why.
func TestUpdateWarnsWhenHandHomeIsNotAFleetHome(t *testing.T) {
	setFakeExecutable(t)
	home := t.TempDir()
	t.Chdir(home)
	notAHome := t.TempDir()
	t.Setenv("HAND_HOME", notAHome)

	fixture := buildUpdateFixture(t, newBinaryFixtureBytes(t))
	writeFakeGHUpdate(t, "v0.5.0", "fixed the frobnicator", fixture)

	cmd := newUpdateCmd(directStableBuild("v0.1.0"))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got %v, want a successful update despite the misconfigured HAND_HOME", err)
	}

	got := out.String()
	if !strings.Contains(got, "fleet_reconcile: failed\n") {
		t.Fatalf("got %q, want fleet_reconcile=failed", got)
	}
	quotedHome := fmt.Sprintf("%q", notAHome)
	if !strings.Contains(errOut.String(), "warning: resolve fleet home for reconciliation:") || !strings.Contains(errOut.String(), quotedHome) {
		t.Fatalf("got stderr %q, want a warning naming %q", errOut.String(), quotedHome)
	}
}

// The binary is already replaced before the handoff runs, so the new binary exiting nonzero
// must not turn a successful update into a nonzero exit or pretend the binary swap failed too.
func TestUpdateReportsFailedFleetReconcileWhenTheNewBinaryExitsNonzero(t *testing.T) {
	execPath := setFakeExecutable(t)
	execDir := filepath.Dir(execPath)
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeNewBinaryInitFailure(t, execDir, 1, "simulated init failure\n")

	fixture := buildUpdateFixture(t, newBinaryFixtureBytes(t))
	writeFakeGHUpdate(t, "v0.5.0", "fixed the frobnicator", fixture)

	cmd := newUpdateCmd(directStableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got %v, want a successful update despite the new binary's init failing", err)
	}

	got := out.String()
	if !strings.Contains(got, "updated: true\n") {
		t.Fatalf("got %q, want the binary replacement itself still reported as succeeded", got)
	}
	if !strings.Contains(got, "fleet_reconcile: failed\n") {
		t.Fatalf("got %q, want fleet_reconcile=failed", got)
	}
	if !strings.Contains(got, "simulated init failure") {
		t.Fatalf("got %q, want the new binary's own error surfaced", got)
	}
}

func TestUpdateDegradesGracefullyWithoutReleaseNotes(t *testing.T) {
	execPath := setFakeExecutable(t)
	execDir := filepath.Dir(execPath)
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeNewBinaryInitBehavior(t, execDir, fakeCanonicalAgentsMD)

	fixture := buildUpdateFixture(t, newBinaryFixtureBytes(t))
	writeFakeGHUpdate(t, "v0.5.0", "", fixture)

	cmd := newUpdateCmd(directStableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "fleet_reconcile: ok\n") || !strings.Contains(got, "notes[0]:\n") {
		t.Fatalf("got %q, want fleet_reconcile=ok and an empty notes list", got)
	}
}

func checkedUpdateDoc(current, latest string, available bool) string {
	currentChannel := "stable"
	if current == "dev" {
		currentChannel = "dev"
	}
	doc := "current: " + current + "\n" +
		"current_channel: " + currentChannel + "\n" +
		"current_commit: unknown\n" +
		"distribution: \"\"\n" +
		"latest: " + latest + "\n" +
		"latest_channel: stable\n" +
		"latest_commit: fedcba9876543210fedcba9876543210fedcba98\n" +
		fmt.Sprintf("update_available: %t\n", available) +
		"updated: false\n" +
		"fleet_reconcile: not-applicable\n" +
		"notes[0]:\n"
	if !available {
		return doc
	}
	return doc + "help[1]:\n" +
		"  - Run `hand update` to install " + latest + ", which also reconciles this home's generated fleet surfaces via hand init\n"
}

func TestUpdateCheckReportsAvailableUpdate(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	writeFakeGHReleaseView(t, "v0.5.0")

	cmd := newUpdateCmd(stableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := checkedUpdateDoc("v0.1.0", "v0.5.0", true)
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

// Up to date is an answer, not an absence: it renders the same schema as an
// available update, differing only in the value of update_available.
func TestUpdateCheckReportsUpToDate(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	writeFakeGHReleaseView(t, "v0.1.0")

	cmd := newUpdateCmd(stableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := checkedUpdateDoc("v0.1.0", "v0.1.0", false)
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestUpdateWithoutCheckSkipsInstallWhenUpToDate(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	writeFakeGHReleaseView(t, "v0.1.0")

	cmd := newUpdateCmd(stableBuild("v0.1.0"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := checkedUpdateDoc("v0.1.0", "v0.1.0", false)
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestUpdateCheckReportsAvailableUpdateForDevBuild(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	writeFakeGHReleaseView(t, "v0.5.0")

	cmd := newUpdateCmd(devBuild("dev"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := checkedUpdateDoc("dev", "v0.5.0", true)
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestUpdatePropagatesLatestTagError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	cmd := newUpdateCmd(directStableBuild("v0.1.0"))
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want error when gh is unreachable")
	}
}
