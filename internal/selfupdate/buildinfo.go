package selfupdate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var readExecutableBuildInfo = ReadExecutableBuildInfo

var verifyExecutableBuildInfo = verifyExecutableBuildInfoDefault

// ReadExecutableBuildInfo asks an absolute Hand binary for its Fleet-independent
// identity before a caller gives it ownership of an install path.
func ReadExecutableBuildInfo(ctx context.Context, path string) (BuildInfo, error) {
	if !filepath.IsAbs(path) {
		return BuildInfo{}, fmt.Errorf("executable path must be absolute: %q", path)
	}
	out, err := exec.CommandContext(ctx, path, "build-info").CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message != "" {
			return BuildInfo{}, fmt.Errorf("run %s build-info: %w: %s", path, err, message)
		}
		return BuildInfo{}, fmt.Errorf("run %s build-info: %w", path, err)
	}
	return parseBuildInfoOutput(strings.NewReader(string(out)))
}

func parseBuildInfoOutput(input io.Reader) (BuildInfo, error) {
	fields := make(map[string]string, 4)
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "version" && key != "channel" && key != "commit" && key != "distribution" {
			continue
		}
		if _, exists := fields[key]; exists {
			return BuildInfo{}, fmt.Errorf("build-info contains duplicate %s", key)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return BuildInfo{}, fmt.Errorf("decode build-info %s: %w", key, err)
			}
			value = decoded
		}
		fields[key] = value
	}
	if err := scanner.Err(); err != nil {
		return BuildInfo{}, fmt.Errorf("read build-info: %w", err)
	}
	for _, key := range []string{"version", "channel", "commit", "distribution"} {
		if _, ok := fields[key]; !ok {
			return BuildInfo{}, fmt.Errorf("build-info is missing %s", key)
		}
	}
	return BuildInfo{
		Version:      fields["version"],
		Channel:      fields["channel"],
		Commit:       fields["commit"],
		Distribution: fields["distribution"],
	}, nil
}

// SameBuild compares the identity fields that make a release executable exact.
func SameBuild(a, b BuildInfo) bool {
	return strings.TrimPrefix(strings.TrimSpace(a.Version), "v") == strings.TrimPrefix(strings.TrimSpace(b.Version), "v") &&
		a.Channel == b.Channel &&
		strings.EqualFold(strings.TrimSpace(a.Commit), strings.TrimSpace(b.Commit)) &&
		a.Distribution == b.Distribution
}

func verifyExecutableBuildInfoDefault(ctx context.Context, path string, want BuildInfo) error {
	got, err := readExecutableBuildInfo(ctx, path)
	if err != nil {
		return fmt.Errorf("verify build identity for %s: %w", path, err)
	}
	if !SameBuild(got, want) {
		return fmt.Errorf("build identity mismatch for %s: got %#v, want %#v", path, got, want)
	}
	return nil
}
