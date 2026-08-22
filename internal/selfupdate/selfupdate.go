package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/atqamz/hand/internal/integration"
)

const Repo = "atqamz/hand"

const binaryName = "hand"

func AssetName() string {
	return assetName(runtime.GOOS, runtime.GOARCH)
}

func assetName(goos, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("%s-%s-%s.zip", binaryName, goos, goarch)
	}
	return fmt.Sprintf("%s-%s-%s.tar.gz", binaryName, goos, goarch)
}

func archiveBinaryName(goos string) string {
	if goos == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}

func latestTag(ctx context.Context, repo string) (string, error) {
	if selfUpdateTestFallback {
		out, err := runTestGH(ctx, "release", "view", "--repo", repo, "--json", "tagName", "--jq", ".tagName")
		if err != nil {
			return "", fmt.Errorf("query latest release: %w", err)
		}
		return out, nil
	}
	var release githubRelease
	err := githubAPI(ctx, repo, "/releases/latest", &release)
	if err != nil {
		return "", fmt.Errorf("query latest release: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("query latest release: empty tag name")
	}
	return release.TagName, nil
}

// ReleaseNotes returns the release body for tag. Callers should treat an error as "no
// notes available" rather than fail the update over it: the version replacement already
// succeeded, and missing or empty notes shouldn't undo that.
func ReleaseNotes(repo, tag string) (string, error) {
	return releaseNotes(context.Background(), repo, tag)
}

func releaseNotes(ctx context.Context, repo, tag string) (string, error) {
	if selfUpdateTestFallback {
		out, err := runTestGH(ctx, "release", "view", tag, "--repo", repo, "--json", "body", "--jq", ".body")
		if err != nil {
			return "", fmt.Errorf("query release notes: %w", err)
		}
		return out, nil
	}
	var release githubRelease
	err := githubAPI(ctx, repo, "/releases/tags/"+url.PathEscape(tag), &release)
	if err != nil {
		return "", fmt.Errorf("query release notes: %w", err)
	}
	return release.Body, nil
}

// IsNewer reports whether latest is newer than current. A current version that
// doesn't parse as semver (e.g. "dev", an unversioned local build) is always
// considered outdated, since there's no way to prove it already matches latest.
func IsNewer(latest, current string) (bool, error) {
	lMajor, lMinor, lPatch, err := parseSemver(latest)
	if err != nil {
		return false, fmt.Errorf("parse latest version %q: %w", latest, err)
	}
	cMajor, cMinor, cPatch, err := parseSemver(current)
	if err != nil {
		return true, nil
	}
	if lMajor != cMajor {
		return lMajor > cMajor, nil
	}
	if lMinor != cMinor {
		return lMinor > cMinor, nil
	}
	return lPatch > cPatch, nil
}

type VersionRelation string

const (
	VersionOlder VersionRelation = "older"
	VersionSame  VersionRelation = "same"
	VersionNewer VersionRelation = "newer"
)

func CompareVersions(current, target string) (VersionRelation, error) {
	currentMajor, currentMinor, currentPatch, err := parseSemver(current)
	if err != nil {
		return "", fmt.Errorf("parse current version %q: %w", current, err)
	}
	targetMajor, targetMinor, targetPatch, err := parseSemver(target)
	if err != nil {
		return "", fmt.Errorf("parse target version %q: %w", target, err)
	}
	if currentMajor != targetMajor {
		if currentMajor < targetMajor {
			return VersionOlder, nil
		}
		return VersionNewer, nil
	}
	if currentMinor != targetMinor {
		if currentMinor < targetMinor {
			return VersionOlder, nil
		}
		return VersionNewer, nil
	}
	if currentPatch != targetPatch {
		if currentPatch < targetPatch {
			return VersionOlder, nil
		}
		return VersionNewer, nil
	}
	return VersionSame, nil
}

func parseSemver(s string) (major, minor, patch int, err error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid version %q", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid version %q", s)
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], nil
}

// ExecutableOverride lets tests point Apply at a fake binary path instead of
// the real test binary produced by `go test`.
var ExecutableOverride = os.Executable

// Apply downloads the exact target release, verifies its checksum and build identity, and replaces
// the running binary with a complete staged file in the executable's directory.
func Apply(repo string, target Target) error {
	execPath, err := ExecutableOverride()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve running binary path: %w", err)
	}

	assetName := AssetName()

	tmpDir, err := os.MkdirTemp("", "hand-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := downloadAssets(context.Background(), repo, target.Tag, tmpDir, assetName, "checksums.txt"); err != nil {
		return fmt.Errorf("download release assets: %w", err)
	}
	if err := verifyChecksum(tmpDir, assetName); err != nil {
		return err
	}

	staged, err := os.CreateTemp(filepath.Dir(execPath), ".hand-update-*")
	if err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}
	tmpBinary := staged.Name()
	defer func() { _ = os.Remove(tmpBinary) }()
	if err := staged.Close(); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	if err := extractBinary(filepath.Join(tmpDir, assetName), archiveBinaryName(runtime.GOOS), tmpBinary); err != nil {
		return err
	}
	want := BuildInfo{Version: target.Version, Channel: target.Channel, Commit: target.Commit, Distribution: DistributionGitHub}
	if err := verifyExecutableBuildInfo(context.Background(), tmpBinary, want); err != nil {
		return fmt.Errorf("verify staged build identity: %w", err)
	}

	replacement, err := replaceAdoptedExecutable(execPath, tmpBinary)
	if err != nil {
		return fmt.Errorf("replace running binary: %w", err)
	}
	if err := verifyExecutableBuildInfo(context.Background(), execPath, want); err != nil {
		if rollbackErr := replacement.rollback(); rollbackErr != nil {
			return fmt.Errorf("verify selected build identity: %w; rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("verify selected build identity: %w", err)
	}
	if err := replacement.commit(); err != nil {
		return fmt.Errorf("clean up previous Hand after selecting %s: %w", execPath, err)
	}
	return nil
}

func downloadAssets(ctx context.Context, repo, tag, dir string, patterns ...string) error {
	if selfUpdateTestFallback {
		args := []string{"release", "download", tag, "--repo", repo, "--dir", dir, "--clobber"}
		for _, p := range patterns {
			args = append(args, "--pattern", p)
		}
		_, err := runTestGH(ctx, args...)
		return err
	}
	var release githubRelease
	if err := githubAPI(ctx, repo, "/releases/tags/"+url.PathEscape(tag), &release); err != nil {
		return fmt.Errorf("resolve release assets: %w", err)
	}
	for _, pattern := range patterns {
		var asset *githubAsset
		for i := range release.Assets {
			if release.Assets[i].Name == pattern {
				asset = &release.Assets[i]
				break
			}
		}
		if asset == nil {
			return fmt.Errorf("release %s has no asset %s", tag, pattern)
		}
		if err := downloadGitHubAsset(ctx, asset.BrowserDownloadURL, filepath.Join(dir, asset.Name)); err != nil {
			return err
		}
	}
	return nil
}

func verifyChecksum(dir, assetName string) error {
	data, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return fmt.Errorf("read checksums.txt: %w", err)
	}

	want := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == assetName {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", assetName)
	}

	f, err := os.Open(filepath.Join(dir, assetName))
	if err != nil {
		return fmt.Errorf("open %s: %w", assetName, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", assetName, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: want %s, got %s", assetName, want, got)
	}
	return nil
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func githubAPI(ctx context.Context, repo, suffix string, result any) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid GitHub repository %q", repo)
	}
	endpoint := "https://api.github.com/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + suffix
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create GitHub API request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "hand")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("request GitHub API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API: HTTP %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}

func downloadGitHubAsset(ctx context.Context, rawURL, path string) error {
	const maxAssetSize = 2 << 30
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create release asset request: %w", err)
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "hand")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download release asset: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download release asset: HTTP %s", response.Status)
	}
	if response.ContentLength > maxAssetSize {
		return fmt.Errorf("download release asset is too large: %d bytes", response.ContentLength)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hand-release-*")
	if err != nil {
		return fmt.Errorf("create release asset %s: %w", filepath.Base(path), err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	limited := io.LimitReader(response.Body, maxAssetSize+1)
	n, copyErr := io.Copy(file, limited)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write release asset %s: %w", filepath.Base(path), copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close release asset %s: %w", filepath.Base(path), closeErr)
	}
	if n > maxAssetSize {
		return fmt.Errorf("download release asset exceeds %d bytes", maxAssetSize)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish release asset %s: %w", filepath.Base(path), err)
	}
	return nil
}

func runTestGH(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, err := integration.Run(ctx, "github/gh", "", args...)
	if err != nil {
		message := strings.TrimSpace(string(stderr))
		if message != "" {
			return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, message)
		}
		return "", fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(stdout)), nil
}
