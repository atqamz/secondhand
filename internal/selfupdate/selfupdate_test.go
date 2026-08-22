package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/faketool"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.4.0", "v0.3.1", true},
		{"v0.3.1", "v0.4.0", false},
		{"v0.3.1", "v0.3.1", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.2", "v0.1.1", true},
		{"v0.4.0", "dev", true},
		{"0.4.0", "0.3.1", true},
	}
	for _, c := range cases {
		got, err := IsNewer(c.latest, c.current)
		if err != nil {
			t.Fatalf("IsNewer(%q, %q): %v", c.latest, c.current, err)
		}
		if got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestIsNewerRejectsInvalidLatest(t *testing.T) {
	if _, err := IsNewer("not-a-version", "v0.3.1"); err == nil {
		t.Fatal("want error for unparseable latest version")
	}
}

func TestAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch string
		want         string
	}{
		{goos: "linux", goarch: "amd64", want: "hand-linux-amd64.tar.gz"},
		{goos: "linux", goarch: "arm64", want: "hand-linux-arm64.tar.gz"},
		{goos: "darwin", goarch: "amd64", want: "hand-darwin-amd64.tar.gz"},
		{goos: "darwin", goarch: "arm64", want: "hand-darwin-arm64.tar.gz"},
		{goos: "windows", goarch: "amd64", want: "hand-windows-amd64.zip"},
	}
	for _, tt := range tests {
		if got := assetName(tt.goos, tt.goarch); got != tt.want {
			t.Errorf("assetName(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestAssetNameUsesRuntimePlatform(t *testing.T) {
	if got, want := AssetName(), assetName(runtime.GOOS, runtime.GOARCH); got != want {
		t.Fatalf("AssetName() = %q, want %q", got, want)
	}
}

func TestArchiveBinaryName(t *testing.T) {
	for _, tt := range []struct {
		goos, want string
	}{
		{goos: "linux", want: "hand"},
		{goos: "darwin", want: "hand"},
		{goos: "windows", want: "hand.exe"},
	} {
		if got := archiveBinaryName(tt.goos); got != tt.want {
			t.Errorf("archiveBinaryName(%q) = %q, want %q", tt.goos, got, tt.want)
		}
	}
}

// Fakes the two gh calls an update makes, mirroring where real gh puts its output: the
// extracted field alone on stdout, download progress on stderr, and a nonzero exit with
// the reason on stderr for a failure. runGH reads stdout only, so that split matters.
func writeFakeGH(t *testing.T, tag, fixtureDir string) {
	t.Helper()
	portableBin := faketool.Bin(t)
	faketool.GH{Release: faketool.GHRelease{
		Tag: tag, FixtureDir: fixtureDir,
	}}.Install(t, portableBin)
}

func buildFixture(t *testing.T, binaryContent []byte) string {
	t.Helper()
	oldReader := readExecutableBuildInfo
	readExecutableBuildInfo = func(context.Context, string) (BuildInfo, error) {
		return BuildInfo{Version: "0.5.0", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}, nil
	}
	t.Cleanup(func() { readExecutableBuildInfo = oldReader })
	dir := t.TempDir()
	assetName := AssetName()
	archivePath := filepath.Join(dir, assetName)
	entry := archiveEntry{name: archiveBinaryName(runtime.GOOS), content: binaryContent, mode: 0o755}
	if runtime.GOOS == "windows" {
		writeZip(t, archivePath, []archiveEntry{entry})
	} else {
		writeTarGz(t, archivePath, []archiveEntry{entry})
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

func stableTarget(version string) Target {
	return Target{Channel: ChannelStable, Tag: version, Version: version, Commit: stableTestCommit}
}

func TestApplyReplacesRunningBinary(t *testing.T) {
	fixture := buildFixture(t, []byte("new binary contents"))
	writeFakeGH(t, "v0.5.0", fixture)

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hand")
	if err := os.WriteFile(execPath, []byte("old binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}

	restore := ExecutableOverride
	ExecutableOverride = func() (string, error) { return execPath, nil }
	defer func() { ExecutableOverride = restore }()

	if err := Apply("atqamz/hand", stableTarget("v0.5.0")); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary contents" {
		t.Fatalf("got %q, want new binary contents", got)
	}

	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("got mode %v, want 0755", info.Mode().Perm())
	}

	entries, err := os.ReadDir(execDir)
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := 1
	if runtime.GOOS == "windows" {
		wantEntries = 2
	}
	if len(entries) != wantEntries {
		t.Fatalf("got %d entries in install dir, want %d", len(entries), wantEntries)
	}
	if runtime.GOOS == "windows" {
		backupCount := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".hand-update-") && strings.HasSuffix(entry.Name(), ".old.exe") {
				backupCount++
			}
		}
		if backupCount != 1 {
			t.Fatalf("got %d updater backups, want one", backupCount)
		}
	}
}

func TestApplyRejectsStagedBuildIdentityMismatch(t *testing.T) {
	fixture := buildFixture(t, []byte("new binary contents"))
	writeFakeGH(t, "v0.5.0", fixture)

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hand")
	if err := os.WriteFile(execPath, []byte("old binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldReader := readExecutableBuildInfo
	readExecutableBuildInfo = func(context.Context, string) (BuildInfo, error) {
		return BuildInfo{Version: "0.0.1", Channel: ChannelStable, Commit: edgeTestCommit, Distribution: DistributionGitHub}, nil
	}
	t.Cleanup(func() { readExecutableBuildInfo = oldReader })
	oldOverride := ExecutableOverride
	ExecutableOverride = func() (string, error) { return execPath, nil }
	t.Cleanup(func() { ExecutableOverride = oldOverride })

	err := Apply("atqamz/hand", stableTarget("v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), "build identity mismatch") {
		t.Fatalf("Apply error = %v, want build identity mismatch", err)
	}
	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary contents" {
		t.Fatalf("executable = %q, want old contents after identity refusal", got)
	}
}

func TestApplyRollsBackWhenSelectedBuildIdentityVerificationFails(t *testing.T) {
	fixture := buildFixture(t, []byte("new binary contents"))
	writeFakeGH(t, "v0.5.0", fixture)

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hand")
	if err := os.WriteFile(execPath, []byte("old binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldReader := readExecutableBuildInfo
	reads := 0
	readExecutableBuildInfo = func(context.Context, string) (BuildInfo, error) {
		reads++
		if reads == 1 {
			return BuildInfo{Version: "0.5.0", Channel: ChannelStable, Commit: stableTestCommit, Distribution: DistributionGitHub}, nil
		}
		return BuildInfo{Version: "0.5.0", Channel: ChannelStable, Commit: edgeTestCommit, Distribution: DistributionGitHub}, nil
	}
	t.Cleanup(func() { readExecutableBuildInfo = oldReader })
	oldOverride := ExecutableOverride
	ExecutableOverride = func() (string, error) { return execPath, nil }
	t.Cleanup(func() { ExecutableOverride = oldOverride })

	err := Apply("atqamz/hand", stableTarget("v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), "verify selected build identity") {
		t.Fatalf("Apply error = %v, want selected identity verification failure", err)
	}
	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary contents" {
		t.Fatalf("executable = %q, want old contents after rollback", got)
	}
}

func TestApplyLeavesNoStagedFileWhenExtractionFails(t *testing.T) {
	fixture := t.TempDir()
	assetName := AssetName()
	payload := []byte("not a gzip stream")
	if err := os.WriteFile(filepath.Join(fixture, assetName), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)
	if err := os.WriteFile(filepath.Join(fixture, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeGH(t, "v0.5.0", fixture)

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hand")
	if err := os.WriteFile(execPath, []byte("old binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}

	restore := ExecutableOverride
	ExecutableOverride = func() (string, error) { return execPath, nil }
	defer func() { ExecutableOverride = restore }()

	err := Apply("atqamz/hand", stableTarget("v0.5.0"))
	if err == nil {
		t.Fatal("want error when the asset is not a valid archive")
	}
	wantError := "open gzip stream"
	if runtime.GOOS == "windows" {
		wantError = "open zip"
	}
	if !strings.Contains(err.Error(), wantError) {
		t.Fatalf("error = %q, want %q", err, wantError)
	}

	entries, err := os.ReadDir(execDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries in install dir, want no staged leftovers", len(entries))
	}
	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary contents" {
		t.Fatalf("got %q, want the running binary left untouched", got)
	}
}

func TestVerifyChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	assetName := AssetName()
	if err := os.WriteFile(filepath.Join(dir, assetName), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("deadbeef  "+assetName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(dir, assetName); err == nil {
		t.Fatal("want checksum mismatch error")
	}
}

func TestApplyLeavesBinaryUnchangedOnChecksumMismatch(t *testing.T) {
	fixture := t.TempDir()
	assetName := AssetName()
	if err := os.WriteFile(filepath.Join(fixture, assetName), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "checksums.txt"), []byte("deadbeef  "+assetName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeGH(t, "v0.5.0", fixture)

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hand")
	if err := os.WriteFile(execPath, []byte("old binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := ExecutableOverride
	ExecutableOverride = func() (string, error) { return execPath, nil }
	t.Cleanup(func() { ExecutableOverride = restore })

	err := Apply("atqamz/hand", stableTarget("v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch for "+assetName) {
		t.Fatalf("error = %v, want checksum mismatch for %s", err, assetName)
	}
	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary contents" {
		t.Fatalf("got %q, want the installed binary unchanged", got)
	}
	entries, err := os.ReadDir(execDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d install-directory entries, want only canonical executable", len(entries))
	}
}

func TestExtractBinaryMissingFromArchive(t *testing.T) {
	dir := t.TempDir()
	var tarBuf bytes.Buffer
	gz := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "other-file", Mode: 0o644, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(archivePath, tarBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := extractBinary(archivePath, binaryName, filepath.Join(dir, "out")); err == nil {
		t.Fatal("want error when binary missing from archive")
	}
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.tar.gz")
	writeTarGz(t, archivePath, []archiveEntry{
		{name: "other-before", content: []byte("before"), mode: 0o644},
		{name: "nested/hand", content: []byte("tar contents"), mode: 0o755},
		{name: "other-after", content: []byte("after"), mode: 0o644},
	})

	outPath := filepath.Join(dir, "out")
	if err := extractBinary(archivePath, "hand", outPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "tar contents" {
		t.Fatalf("got %q, want tar contents", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("got mode %o, want 0755", got)
		}
	}
}

func TestExtractBinaryFromZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.zip")
	writeZip(t, archivePath, []archiveEntry{
		{name: "other-before", content: []byte("before"), mode: 0o644},
		{name: "nested/hand.exe", content: []byte("zip contents"), mode: 0o755},
		{name: "other-after", content: []byte("after"), mode: 0o644},
	})

	outPath := filepath.Join(dir, "out")
	if err := extractBinary(archivePath, "hand.exe", outPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "zip contents" {
		t.Fatalf("got %q, want zip contents", got)
	}
}

func TestExtractBinaryRejectsCorruptGzip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(archivePath, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := extractBinary(archivePath, "hand", filepath.Join(dir, "out")); err == nil {
		t.Fatal("want corrupt gzip error")
	}
}

func TestExtractBinaryRejectsCorruptZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(archivePath, []byte("not zip"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := extractBinary(archivePath, "hand.exe", filepath.Join(dir, "out")); err == nil {
		t.Fatal("want corrupt zip error")
	}
}

func TestExtractBinaryRejectsUnsupportedArchive(t *testing.T) {
	if err := extractBinary("archive.tar", "hand", filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("want unsupported archive error")
	}
}

type archiveEntry struct {
	name    string
	content []byte
	mode    int64
}

func writeTarGz(t *testing.T, path string, entries []archiveEntry) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, entries []archiveEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(os.FileMode(entry.mode))
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
