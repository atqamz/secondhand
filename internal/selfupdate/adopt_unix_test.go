//go:build !windows

package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptPreservesDirectInstallWhenHardLinksAreUnavailable(t *testing.T) {
	want := BuildInfo{Version: "1.2.3", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}
	source := writeIdentityExecutable(t, want, "new-source")
	target := testHandPath(t.TempDir())
	writeIdentityExecutableAt(t, target, BuildInfo{Version: "1.0.0", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}, "old-target")

	originalLink := linkFile
	linkFile = func(string, string) error { return errors.New("hard links unavailable") }
	t.Cleanup(func() { linkFile = originalLink })

	if _, err := Adopt(context.Background(), source, target, want); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("selected Hand missing: %v", err)
	}
	if err := verifyExecutableBuildInfoDefault(context.Background(), target, want); err != nil {
		t.Fatalf("selected Hand identity: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".hand-adopt-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("rollback backups = %v, want none after commit", backups)
	}
}
