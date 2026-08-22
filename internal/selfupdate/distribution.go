package selfupdate

import "runtime/debug"

const (
	DistributionGitHub        = "github"
	DistributionInstallScript = "install-script"
	DistributionBrew          = "brew"
	DistributionWinget        = "winget"
	DistributionNpm           = "npm"
	DistributionNix           = "nix"
	DistributionDeb           = "deb"
	DistributionRpm           = "rpm"
	DistributionAur           = "aur"
	DistributionGo            = "go"
	DistributionSource        = "source"
)

// A var so a test can simulate the one signal that tells a `go install`-fetched
// binary from a bare `go build`/`go run` in a source checkout.
var readBuildInfo = debug.ReadBuildInfo

func detectDistribution() string {
	info, ok := readBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return DistributionGo
	}
	return DistributionSource
}

var handOwned = map[string]bool{
	DistributionGitHub:        true,
	DistributionInstallScript: true,
}

// CanSelfUpdate reports whether hand's own updater may replace the running binary in
// place. A package-manager-owned, go-installed, or source build must instead be
// upgraded through the channel that placed it.
func CanSelfUpdate(distribution string) bool {
	return handOwned[distribution]
}

var upgradeCommands = map[string]string{
	DistributionBrew:   "brew upgrade atqamz/tap/hand",
	DistributionWinget: "winget upgrade Atqamz.Hand",
	DistributionNpm:    "npm update -g @atqamz/hand",
	DistributionNix:    "nix profile upgrade hand",
	DistributionDeb:    "download and install the latest hand-*.deb from the GitHub release",
	DistributionRpm:    "download and install the latest hand-*.rpm from the GitHub release",
	DistributionAur:    "update hand-bin with your AUR helper, e.g. yay -Syu hand-bin",
	DistributionGo:     "go install github.com/atqamz/hand@latest",
	DistributionSource: "rebuild from source: git pull && make build",
}

// UpgradeCommand names the command that installs a newer build through the
// distribution that owns it, for a distribution CanSelfUpdate refuses.
func UpgradeCommand(distribution string) string {
	if command, ok := upgradeCommands[distribution]; ok {
		return command
	}
	return "reinstall hand through the channel it came from"
}
