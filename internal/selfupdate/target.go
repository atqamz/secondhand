package selfupdate

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const (
	ChannelDev    = "dev"
	ChannelStable = "stable"
	ChannelEdge   = "edge"
)

type BuildInfo struct {
	Version      string
	Channel      string
	Commit       string
	Distribution string
}

type Target struct {
	Channel string
	Tag     string
	Version string
	Commit  string
}

func NormalizeBuildInfo(version, channel, commit, distribution string) BuildInfo {
	if channel != ChannelStable && channel != ChannelEdge {
		channel = ChannelDev
	}
	if distribution == "" {
		distribution = detectDistribution()
	}
	return BuildInfo{Version: version, Channel: channel, Commit: commit, Distribution: distribution}
}

func ResolveTarget(repo, channel string) (Target, error) {
	return resolveTarget(context.Background(), repo, channel)
}

// DisplayCommit renders an embedded or resolved commit for output, naming the
// absence of one rather than leaving an empty field.
func DisplayCommit(commit string) string {
	if commit == "" {
		return "unknown"
	}
	return commit
}

// Rebuilds a Target from remembered values. Only edge carries a tag that differs
// from its version, and it is always the rolling tag itself.
func cachedTarget(channel, version, commit string) Target {
	tag := version
	if channel == ChannelEdge {
		tag = ChannelEdge
	}
	return Target{Channel: channel, Tag: tag, Version: version, Commit: commit}
}

func resolveTarget(ctx context.Context, repo, channel string) (Target, error) {
	if err := validateChannel(channel); err != nil {
		return Target{}, err
	}

	switch channel {
	case ChannelStable:
		tag, err := latestTag(ctx, repo)
		if err != nil {
			return Target{}, err
		}
		commit, err := stableCommit(ctx, repo, tag)
		if err != nil {
			return Target{}, err
		}
		return Target{Channel: ChannelStable, Tag: tag, Version: tag, Commit: commit}, nil
	case ChannelEdge:
		commit, err := edgeCommit(ctx, repo)
		if err != nil {
			return Target{}, err
		}
		if !validCommit(commit) {
			return Target{}, fmt.Errorf("invalid edge commit %q", commit)
		}
		return Target{
			Channel: ChannelEdge,
			Tag:     ChannelEdge,
			Version: "edge." + shortCommit(commit),
			Commit:  commit,
		}, nil
	}
	return Target{}, fmt.Errorf("invalid release channel %q", channel)
}

func stableCommit(ctx context.Context, repo, tag string) (string, error) {
	if selfUpdateTestFallback {
		out, err := runTestGH(ctx, "api", "repos/"+repo+"/commits/"+tag, "--jq", ".sha")
		if err != nil {
			return "", fmt.Errorf("query stable release commit: %w", err)
		}
		if !validCommit(out) {
			return "", fmt.Errorf("invalid stable release commit %q", out)
		}
		return out, nil
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := githubAPI(ctx, repo, "/commits/"+url.PathEscape(tag), &commit); err != nil {
		return "", fmt.Errorf("query stable release commit: %w", err)
	}
	if !validCommit(commit.SHA) {
		return "", fmt.Errorf("invalid stable release commit %q", commit.SHA)
	}
	return commit.SHA, nil
}

func NeedsUpdate(current BuildInfo, target Target) (bool, error) {
	if err := validateChannel(target.Channel); err != nil {
		return false, err
	}
	current = NormalizeBuildInfo(current.Version, current.Channel, current.Commit, current.Distribution)
	if current.Channel != target.Channel {
		return true, nil
	}

	switch target.Channel {
	case ChannelStable:
		return IsNewer(target.Version, current.Version)
	case ChannelEdge:
		if !validCommit(target.Commit) {
			return false, fmt.Errorf("invalid edge commit %q", target.Commit)
		}
		return !validCommit(current.Commit) || !strings.EqualFold(current.Commit, target.Commit), nil
	default:
		return false, fmt.Errorf("invalid release channel %q", target.Channel)
	}
}

func validateChannel(channel string) error {
	if channel != ChannelStable && channel != ChannelEdge {
		return fmt.Errorf("invalid release channel %q", channel)
	}
	return nil
}

func edgeCommit(ctx context.Context, repo string) (string, error) {
	if selfUpdateTestFallback {
		out, err := runTestGH(ctx, "api", "repos/"+repo+"/commits/edge", "--jq", ".sha")
		if err != nil {
			return "", fmt.Errorf("query edge commit: %w", err)
		}
		return out, nil
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	err := githubAPI(ctx, repo, "/commits/edge", &commit)
	if err != nil {
		return "", fmt.Errorf("query edge commit: %w", err)
	}
	if commit.SHA == "" {
		return "", fmt.Errorf("query edge commit: empty SHA")
	}
	return commit.SHA, nil
}

func validCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	for _, r := range commit {
		hex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !hex {
			return false
		}
	}
	return true
}

func shortCommit(commit string) string {
	if len(commit) <= 12 {
		return commit
	}
	return commit[:12]
}
