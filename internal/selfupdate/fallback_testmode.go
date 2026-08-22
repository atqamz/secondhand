//go:build test

package selfupdate

import "context"

const selfUpdateTestFallback = true

var VerifyExecutableBuildInfoForTests func(context.Context, string, BuildInfo) error

func init() {
	verifyExecutableBuildInfo = func(ctx context.Context, path string, want BuildInfo) error {
		if VerifyExecutableBuildInfoForTests != nil {
			return VerifyExecutableBuildInfoForTests(ctx, path, want)
		}
		return verifyExecutableBuildInfoDefault(ctx, path, want)
	}
}
