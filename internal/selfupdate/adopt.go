package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type AdoptionResult struct {
	Path   string
	Result string
}

// Adopt verifies source, makes an ownership decision for the existing direct Hand, and
// atomically selects the exact source at the selected path. It does not inspect Fleet state.
func Adopt(ctx context.Context, source, target string, want BuildInfo) (AdoptionResult, error) {
	source, err := absolutePath(source)
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("resolve staged Hand path: %w", err)
	}
	target, err = absolutePath(target)
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("resolve Hand install path: %w", err)
	}
	if want.Channel != ChannelStable || !CanSelfUpdate(want.Distribution) {
		return AdoptionResult{}, fmt.Errorf("bootstrap adoption requires a stable direct GitHub build")
	}
	if err := verifyExecutableBuildInfo(ctx, source, want); err != nil {
		return AdoptionResult{}, fmt.Errorf("verify staged build identity: %w", err)
	}

	existing, err := existingHandPath(target)
	if err != nil {
		return AdoptionResult{}, err
	}
	if existing != "" {
		result, err := assessExistingHand(ctx, existing, target, want)
		if err != nil {
			return AdoptionResult{}, err
		}
		if result != "" {
			return AdoptionResult{Path: existing, Result: result}, nil
		}
		target = existing
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return AdoptionResult{}, fmt.Errorf("create Hand install directory %s: %w", filepath.Dir(target), err)
	}
	staged, err := stageExecutable(source, filepath.Dir(target))
	if err != nil {
		return AdoptionResult{}, err
	}
	defer func() { _ = os.Remove(staged) }()

	replacement, err := replaceAdoptedExecutable(target, staged)
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("select Hand at %s: %w", target, err)
	}
	if err := verifyExecutableBuildInfo(ctx, target, want); err != nil {
		if rollbackErr := replacement.rollback(); rollbackErr != nil {
			return AdoptionResult{}, fmt.Errorf("verify selected build identity: %w; rollback failed: %v", err, rollbackErr)
		}
		return AdoptionResult{}, fmt.Errorf("verify selected build identity: %w", err)
	}
	if err := replacement.commit(); err != nil {
		return AdoptionResult{}, fmt.Errorf("clean up previous Hand after selecting %s: %w", target, err)
	}
	return AdoptionResult{Path: target, Result: "installed"}, nil
}

func absolutePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %q", path)
	}
	return filepath.Clean(path), nil
}

func existingHandPath(target string) (string, error) {
	if _, err := os.Lstat(target); err == nil {
		return target, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect existing Hand at %s: %w", target, err)
	}
	path, err := exec.LookPath(binaryName)
	if err != nil {
		return "", nil
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Hand on PATH: %w", err)
	}
	return filepath.Clean(path), nil
}

func assessExistingHand(ctx context.Context, path, target string, want BuildInfo) (string, error) {
	identity, err := ReadExecutableBuildInfo(ctx, path)
	if err != nil {
		if path != target || !isDefaultDirectPath(target) {
			return "", fmt.Errorf("hand at %s has no verifiable build identity; refusing to replace unknown ownership", path)
		}
		legacyVersion, legacyErr := readLegacyVersion(ctx, path)
		if legacyErr != nil {
			return "", fmt.Errorf("hand at %s has no verifiable build identity; refusing to replace unknown ownership: %w", path, legacyErr)
		}
		relation, compareErr := CompareVersions(legacyVersion, want.Version)
		if compareErr != nil {
			return "", fmt.Errorf("compare legacy Hand version %q with %q: %w", legacyVersion, want.Version, compareErr)
		}
		if relation == VersionNewer {
			return "", fmt.Errorf("refusing to downgrade hand from %s to %s", legacyVersion, want.Version)
		}
		return "", nil
	}
	if !knownDistribution(identity.Distribution) {
		return "", fmt.Errorf("hand ownership is unknown for distribution %s; refusing to replace it", identity.Distribution)
	}
	if !CanSelfUpdate(identity.Distribution) {
		return "", fmt.Errorf("hand will not replace a %s build; %s", identity.Distribution, UpgradeCommand(identity.Distribution))
	}
	if identity.Channel != ChannelStable {
		return "", fmt.Errorf("hand is %s, not the stable release %s; refusing a silent channel change", identity.Channel, want.Version)
	}
	relation, err := CompareVersions(identity.Version, want.Version)
	if err != nil {
		return "", fmt.Errorf("compare installed Hand version %q with %q: %w", identity.Version, want.Version, err)
	}
	if relation == VersionNewer {
		return "", fmt.Errorf("refusing to downgrade hand from %s to %s", identity.Version, want.Version)
	}
	if relation == VersionSame && SameBuild(identity, want) {
		return "reused", nil
	}
	return "", nil
}

func knownDistribution(distribution string) bool {
	switch distribution {
	case DistributionGitHub, DistributionInstallScript, DistributionBrew, DistributionWinget,
		DistributionNpm, DistributionNix, DistributionDeb, DistributionRpm, DistributionAur,
		DistributionGo, DistributionSource:
		return true
	default:
		return false
	}
}

func readLegacyVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(out))
	if fields := strings.Fields(version); len(fields) > 0 {
		version = fields[len(fields)-1]
	}
	return version, nil
}

func isDefaultDirectPath(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if filepath.Separator == '\\' {
		local := os.Getenv("LOCALAPPDATA")
		return local != "" && filepath.Clean(path) == filepath.Join(local, "hand", "hand.exe")
	}
	return filepath.Clean(path) == filepath.Join(home, ".local", "bin", binaryName)
}

func stageExecutable(source, dir string) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("open staged Hand %s: %w", source, err)
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return "", fmt.Errorf("stat staged Hand %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("staged Hand %s is not a regular file", source)
	}
	file, err := os.CreateTemp(dir, ".hand-adopt-*")
	if err != nil {
		return "", fmt.Errorf("stage Hand in %s: %w", dir, err)
	}
	path := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.ReadFrom(input); err != nil {
		return "", fmt.Errorf("copy staged Hand: %w", err)
	}
	mode := info.Mode().Perm() | 0o111
	if err := file.Chmod(mode); err != nil {
		return "", fmt.Errorf("set staged Hand permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close staged Hand: %w", err)
	}
	remove = false
	return path, nil
}
