package selfupdate

import "testing"

func TestCanSelfUpdateAllowsOnlyHandOwnedDistributions(t *testing.T) {
	for _, distribution := range []string{DistributionGitHub, DistributionInstallScript} {
		if !CanSelfUpdate(distribution) {
			t.Errorf("CanSelfUpdate(%q) = false, want true", distribution)
		}
	}

	for _, distribution := range []string{
		"",
		"unknown",
		DistributionBrew, DistributionWinget, DistributionNpm, DistributionNix,
		DistributionDeb, DistributionRpm, DistributionAur, DistributionGo, DistributionSource,
	} {
		if CanSelfUpdate(distribution) {
			t.Errorf("CanSelfUpdate(%q) = true, want false", distribution)
		}
	}
}

func TestUpgradeCommandNamesEachPackageManagersOwnCommand(t *testing.T) {
	cases := map[string]string{
		DistributionBrew:   "brew upgrade atqamz/tap/hand",
		DistributionWinget: "winget upgrade Atqamz.Hand",
		DistributionNpm:    "npm update -g @atqamz/hand",
		DistributionNix:    "nix profile upgrade hand",
		DistributionGo:     "go install github.com/atqamz/hand@latest",
	}
	for distribution, want := range cases {
		if got := UpgradeCommand(distribution); got != want {
			t.Errorf("UpgradeCommand(%q) = %q, want %q", distribution, got, want)
		}
	}
}

func TestUpgradeCommandFallsBackForAnUnrecognizedDistribution(t *testing.T) {
	if got := UpgradeCommand("some-future-manager"); got == "" {
		t.Fatal("want a non-empty fallback for an unrecognized distribution")
	}
}
