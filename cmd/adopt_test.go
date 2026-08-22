package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/selfupdate"
)

func TestAdoptCommandInstallsTheVerifiedSourceWithoutFleetState(t *testing.T) {
	want := selfupdate.BuildInfo{
		Version:      "1.2.3",
		Channel:      selfupdate.ChannelStable,
		Commit:       "fedcba9876543210fedcba9876543210fedcba98",
		Distribution: selfupdate.DistributionGitHub,
	}
	source := writeAdoptIdentityExecutable(t, want)
	t.Setenv("PATH", t.TempDir())
	target := filepath.Join(t.TempDir(), "bin", "hand")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}

	root := newRootCmd(want)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{
		"adopt",
		"--source", source,
		"--target", target,
		"--version", want.Version,
		"--commit", want.Commit,
	})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("adopt command error = %v", err)
	}
	wantPath := target
	if runtime.GOOS == "windows" {
		wantPath = strconv.Quote(target)
	}
	if !strings.Contains(output.String(), "result: installed") || !strings.Contains(output.String(), "path: "+wantPath) {
		t.Fatalf("adopt output = %q, want installed path", output.String())
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("adopt target: %v", err)
	}
}

func writeAdoptIdentityExecutable(t *testing.T, info selfupdate.BuildInfo) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hand")
	if runtime.GOOS == "windows" {
		path += ".exe"
		ldflags := fmt.Sprintf("-X main.version=%s -X main.channel=%s -X main.commit=%s -X main.distribution=%s", info.Version, info.Channel, info.Commit, info.Distribution)
		_, source, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller failed")
		}
		cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", path, ".")
		cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), ".."))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build Windows Hand fixture: %v\n%s", err, output)
		}
		return path
	}
	content := "#!/bin/sh\n"
	content += "if [ \"$1\" = build-info ]; then\n"
	content += "  printf 'version: " + info.Version + "\\nchannel: " + info.Channel + "\\ncommit: " + info.Commit + "\\ndistribution: " + info.Distribution + "\\n'\n"
	content += "  exit 0\n"
	content += "fi\nexit 1\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
