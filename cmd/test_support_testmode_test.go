//go:build test

package cmd

import (
	"context"

	"github.com/atqamz/hand/internal/selfupdate"
)

func configureSelfUpdateTests() {
	selfupdate.VerifyExecutableBuildInfoForTests = func(context.Context, string, selfupdate.BuildInfo) error { return nil }
}
