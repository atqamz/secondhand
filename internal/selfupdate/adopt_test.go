package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAdoptInstallsAnExactDirectBuildIntoAnEmptyTarget(t *testing.T) {
	goPath := ""
	if runtime.GOOS == "windows" {
		var err error
		goPath, err = exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	want := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}
	source := writeIdentityExecutable(t, want, "source", goPath)
	target := testHandPath(filepath.Join(t.TempDir(), "bin"))

	got, err := Adopt(context.Background(), source, target, want)
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if got.Path != target || got.Result != "installed" {
		t.Fatalf("Adopt() = %#v, want installed %s", got, target)
	}
	if err := verifyExecutableBuildInfoDefault(context.Background(), target, want); err != nil {
		t.Fatalf("installed executable identity: %v", err)
	}
}

func TestAdoptReusesAnExactDirectBuild(t *testing.T) {
	want := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}
	source := writeIdentityExecutable(t, want, "new-source")
	target := testHandPath(t.TempDir())
	writeIdentityExecutableAt(t, target, want, "existing-target")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Adopt(context.Background(), source, target, want)
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if got.Path != target || got.Result != "reused" {
		t.Fatalf("Adopt() = %#v, want reused %s", got, target)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(before) {
		t.Fatalf("target was replaced despite exact identity")
	}
}

func TestAdoptRefusesPackageOwnedBuildBeforeMutation(t *testing.T) {
	want := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}
	source := writeIdentityExecutable(t, want, "source")
	target := testHandPath(t.TempDir())
	writeIdentityExecutableAt(t, target, BuildInfo{Version: "1.0.0", Channel: ChannelStable, Commit: "package", Distribution: DistributionBrew}, "package")

	_, err := Adopt(context.Background(), source, target, want)
	if err == nil || !strings.Contains(err.Error(), "brew") {
		t.Fatalf("Adopt() error = %v, want package ownership refusal", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "package") {
		t.Fatalf("package-owned target changed: %q", content)
	}
}

func TestAdoptRefusesAStagedIdentityMismatchBeforeMutation(t *testing.T) {
	want := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}
	source := writeIdentityExecutable(t, BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: edgeTestCommit, Distribution: DistributionGitHub}, "wrong-source")
	target := testHandPath(t.TempDir())
	writeIdentityExecutableAt(t, target, want, "existing-target")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Adopt(context.Background(), source, target, want)
	if err == nil || !strings.Contains(err.Error(), "build identity mismatch") {
		t.Fatalf("Adopt() error = %v, want staged identity mismatch", err)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("target changed after staged identity refusal")
	}
}

func TestAdoptRefusesANewerDirectBuild(t *testing.T) {
	want := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}
	source := writeIdentityExecutable(t, want, "source")
	target := testHandPath(t.TempDir())
	writeIdentityExecutableAt(t, target, BuildInfo{Version: "1.3.0", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}, "newer")

	_, err := Adopt(context.Background(), source, target, want)
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("Adopt() error = %v, want downgrade refusal", err)
	}
}

func writeIdentityExecutable(t *testing.T, info BuildInfo, marker string, goPath ...string) string {
	path := filepath.Join(t.TempDir(), binaryName)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	writeIdentityExecutableAt(t, path, info, marker, goPath...)
	return path
}

func writeIdentityExecutableAt(t *testing.T, path string, info BuildInfo, marker string, goPath ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		ldflags := fmt.Sprintf("-X main.version=%s -X main.channel=%s -X main.commit=%s -X main.distribution=%s", info.Version, info.Channel, info.Commit, info.Distribution)
		_, source, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller failed")
		}
		goExecutable := "go"
		if len(goPath) > 0 && goPath[0] != "" {
			goExecutable = goPath[0]
		}
		cmd := exec.Command(goExecutable, "build", "-ldflags", ldflags, "-o", path, ".")
		cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build Windows Hand fixture: %v\n%s", err, output)
		}
		return
	}
	content := "#!/bin/sh\n"
	content += "if [ \"$1\" = build-info ]; then\n"
	content += "  printf 'version: " + info.Version + "\\nchannel: " + info.Channel + "\\ncommit: " + info.Commit + "\\ndistribution: " + info.Distribution + "\\n'\n"
	content += "  exit 0\n"
	content += "fi\n"
	content += "# " + marker + "\n"
	content += "exit 1\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func testHandPath(dir string) string {
	name := binaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}
