package release

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const releaseCommit = "0123456789abcdef0123456789abcdef01234567"

type workflowStepDef struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
	Run  string         `yaml:"run"`
}

type workflowJobDef struct {
	Outputs map[string]string `yaml:"outputs"`
	Steps   []workflowStepDef `yaml:"steps"`
}

func TestPrepareReleaseBindsEveryAssetToOneExactRelease(t *testing.T) {
	output := t.TempDir()
	assets := []string{"hand-linux-amd64.tar.gz", "hand-linux-arm64.tar.gz", "hand-darwin-amd64.tar.gz", "hand-darwin-arm64.tar.gz", "hand-windows-amd64.zip"}
	for _, name := range assets {
		if err := os.WriteFile(filepath.Join(output, name), []byte("fixture "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runPrepareRelease(t, output, "v1.2.3", "1.2.3", releaseCommit)
	bootstrap, err := os.Stat(filepath.Join(output, "bootstrap.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && bootstrap.Mode()&0o111 == 0 {
		t.Fatal("generated bootstrap.sh is not executable")
	}

	if _, err := os.Stat(filepath.Join(output, "release-manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("release-manifest.json exists or had an unexpected stat error: %v", err)
	}

	checksums, err := os.ReadFile(filepath.Join(output, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(assets, "bootstrap.sh", "bootstrap.ps1") {
		want := checksumLine(t, filepath.Join(output, name), name)
		if !strings.Contains(string(checksums), want) {
			t.Fatalf("checksums.txt = %q, want %q", checksums, want)
		}
	}
	for _, test := range []struct {
		placeholder string
		asset       string
	}{
		{"HAND_RELEASE_SHA256_LINUX_AMD64", "hand-linux-amd64.tar.gz"},
		{"HAND_RELEASE_SHA256_LINUX_ARM64", "hand-linux-arm64.tar.gz"},
		{"HAND_RELEASE_SHA256_DARWIN_AMD64", "hand-darwin-amd64.tar.gz"},
		{"HAND_RELEASE_SHA256_DARWIN_ARM64", "hand-darwin-arm64.tar.gz"},
		{"HAND_RELEASE_SHA256_WINDOWS_AMD64", "hand-windows-amd64.zip"},
	} {
		bootstrapData, err := os.ReadFile(filepath.Join(output, "bootstrap.sh"))
		if err != nil {
			t.Fatal(err)
		}
		want := checksumLine(t, filepath.Join(output, test.asset), test.asset)
		digest := strings.Fields(want)[0]
		if !strings.Contains(string(bootstrapData), test.placeholder+"='"+digest+"'") {
			t.Fatalf("bootstrap.sh = %q, want embedded digest for %s", bootstrapData, test.asset)
		}
	}

	sh := exec.Command("sh", "-n", filepath.Join(output, "bootstrap.sh"))
	if out, err := sh.CombinedOutput(); err != nil {
		t.Fatalf("sh -n bootstrap.sh: %v: %s", err, out)
	}
}

func TestPrepareReleaseRejectsAReleaseBindingMismatch(t *testing.T) {
	output := t.TempDir()
	if err := os.WriteFile(filepath.Join(output, "hand-linux-amd64.tar.gz"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", filepath.Join(repoRoot(t), ".github", "scripts", "prepare-release.sh"), "release-1.2.3", "1.2.3", releaseCommit, output)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("prepare-release.sh succeeded; output = %s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 {
		t.Fatalf("prepare-release.sh error = %v, output = %s", err, out)
	}
	if !strings.Contains(string(out), "tag") {
		t.Fatalf("prepare-release.sh output = %q, want a tag validation error", out)
	}
}

func TestPreparedBootstrapRunsAgainstOnlyItsEmbeddedArchiveDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix bootstrap behavior is covered on Unix runners")
	}
	output := t.TempDir()
	asset := unixBootstrapAsset(t)
	writePlaceholderReleaseAssets(t, output, asset)

	handDir := t.TempDir()
	hand := filepath.Join(handDir, "hand")
	handScript := "#!/bin/sh\ncase \"$1\" in\n  build-info) printf 'version: 1.2.3\\nchannel: stable\\ncommit: " + releaseCommit + "\\ndistribution: github\\n' ;;\n  adopt) target=; while [ \"$#\" -gt 0 ]; do case \"$1\" in --target) target=$2; shift 2 ;; *) shift ;; esac; done; mkdir -p \"$(dirname \"$target\")\"; cp \"$0\" \"$target\"; chmod 755 \"$target\"; printf 'result: installed\\npath: %s\\n' \"$target\" ;;\n  runtime) printf 'runtime_id: rd2343fe130ff5ba2\\n' ;;\n  init) mkdir -p \"$2/state\"; : > \"$2/state/hand.db\" ;;\n  doctor) printf 'ready: true\\n' ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(hand, []byte(handScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTarGzHand(t, filepath.Join(output, asset), hand)
	runPrepareRelease(t, output, "v1.2.3", "1.2.3", releaseCommit)

	fakeBin := t.TempDir()
	archive := filepath.Join(output, asset)
	assetURL := "https://github.com/atqamz/hand/releases/download/v1.2.3/" + asset
	logPath := filepath.Join(t.TempDir(), "curl.log")
	curl := "#!/bin/sh\nset -eu\nout=\nurl=\nwhile [ \"$#\" -gt 0 ]; do case \"$1\" in -o) out=$2; shift 2 ;; *) url=$1; shift ;; esac; done\nprintf '%s\\n' \"$url\" >> '" + logPath + "'\ncase \"$url\" in\n  " + assetURL + ") cp '" + archive + "' \"$out\" ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	fleet := filepath.Join(t.TempDir(), "fleet")
	cmd := exec.Command("sh", filepath.Join(output, "bootstrap.sh"), "--fleet", fleet)
	cmd.Env = make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "PATH" || name == "HOME" || name == "HAND_INSTALL_DIR" || name == "SECONDHAND_HOME" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+t.TempDir(),
		"HAND_INSTALL_DIR="+filepath.Join(t.TempDir(), "bin"),
		"SECONDHAND_HOME="+filepath.Join(t.TempDir(), ".secondhand"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated bootstrap: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(fleet, "state", "hand.db")); err != nil {
		t.Fatalf("generated bootstrap did not delegate init: %v", err)
	}
	urls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(urls), assetURL+"\n"; got != want {
		t.Fatalf("curl URLs = %q, want %q", got, want)
	}
}

func TestPreparedBootstrapRejectsAValidArchiveWithWrongExecutableIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix bootstrap behavior is covered on Unix runners")
	}
	for _, test := range []struct {
		name, version, channel, commit, distribution, want string
	}{
		{name: "version", version: "9.9.9", channel: "stable", commit: releaseCommit, distribution: "github", want: "build identity mismatch"},
		{name: "channel", version: "1.2.3", channel: "edge", commit: releaseCommit, distribution: "github", want: "build identity mismatch"},
		{name: "commit", version: "1.2.3", channel: "stable", commit: "fedcba9876543210fedcba9876543210fedcba98", distribution: "github", want: "build identity mismatch"},
		{name: "distribution", version: "1.2.3", channel: "stable", commit: releaseCommit, distribution: "brew", want: "build identity mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := t.TempDir()
			asset := unixBootstrapAsset(t)
			writePlaceholderReleaseAssets(t, output, asset)
			hand := buildReleaseFixtureHand(t, test.version, test.channel, test.commit, test.distribution)
			archive := filepath.Join(output, asset)
			writeTarGzHand(t, archive, hand)
			runPrepareRelease(t, output, "v1.2.3", "1.2.3", releaseCommit)

			fakeBin := t.TempDir()
			assetURL := "https://github.com/atqamz/hand/releases/download/v1.2.3/" + asset
			curl := "#!/bin/sh\nset -eu\nout=\nurl=\nwhile [ \"$#\" -gt 0 ]; do case \"$1\" in -o) out=$2; shift 2 ;; *) url=$1; shift ;; esac; done\ncase \"$url\" in\n  " + assetURL + ") cp '" + archive + "' \"$out\" ;;\n  *) exit 1 ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(curl), 0o755); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			fleet := filepath.Join(home, "fleet")
			installDir := filepath.Join(home, "bin")
			cmd := exec.Command("sh", filepath.Join(output, "bootstrap.sh"), "--fleet", fleet)
			cmd.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "HOME="+home, "HAND_INSTALL_DIR="+installDir, "SECONDHAND_HOME="+filepath.Join(home, ".secondhand"))
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), test.want) {
				t.Fatalf("generated bootstrap error = %v, output = %q, want %q", err, out, test.want)
			}
			if _, err := os.Stat(filepath.Join(installDir, "hand")); !os.IsNotExist(err) {
				t.Fatalf("identity failure left an installed Hand: %v", err)
			}
			if _, err := os.Stat(fleet); !os.IsNotExist(err) {
				t.Fatalf("identity failure mutated Fleet target: %v", err)
			}
		})
	}
}

func TestPreparedBootstrapRechecksTheSelectedExecutableIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix bootstrap behavior is covered on Unix runners")
	}
	output := t.TempDir()
	asset := unixBootstrapAsset(t)
	writePlaceholderReleaseAssets(t, output, asset)
	hand := filepath.Join(t.TempDir(), "hand")
	handScript := "#!/bin/sh\ncase \"$1\" in\n  build-info) printf 'version: 1.2.3\\nchannel: stable\\ncommit: " + releaseCommit + "\\ndistribution: github\\n' ;;\n  adopt) target=; while [ \"$#\" -gt 0 ]; do case \"$1\" in --target) target=$2; shift 2 ;; *) shift ;; esac; done; mkdir -p \"$(dirname \"$target\")\"; cat > \"$target\" <<'EOF'\n#!/bin/sh\nif [ \"$1\" = build-info ]; then printf 'version: 1.2.3\\nchannel: stable\\ncommit: fedcba9876543210fedcba9876543210fedcba98\\ndistribution: github\\n'; exit 0; fi\nexit 1\nEOF\nchmod 755 \"$target\"; printf 'result: installed\\npath: %s\\n' \"$target\" ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(hand, []byte(handScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTarGzHand(t, filepath.Join(output, asset), hand)
	runPrepareRelease(t, output, "v1.2.3", "1.2.3", releaseCommit)

	fakeBin := t.TempDir()
	archive := filepath.Join(output, asset)
	assetURL := "https://github.com/atqamz/hand/releases/download/v1.2.3/" + asset
	curl := "#!/bin/sh\nset -eu\nout=\nurl=\nwhile [ \"$#\" -gt 0 ]; do case \"$1\" in -o) out=$2; shift 2 ;; *) url=$1; shift ;; esac; done\ncase \"$url\" in\n  " + assetURL + ") cp '" + archive + "' \"$out\" ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	fleet := filepath.Join(home, "fleet")
	installDir := filepath.Join(home, "bin")
	cmd := exec.Command("sh", filepath.Join(output, "bootstrap.sh"), "--fleet", fleet)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "HOME="+home, "HAND_INSTALL_DIR="+installDir, "SECONDHAND_HOME="+filepath.Join(home, ".secondhand"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "selected Hand commit does not match") {
		t.Fatalf("generated bootstrap error = %v, output = %q, want selected identity refusal", err, out)
	}
	if _, err := os.Stat(filepath.Join(fleet, "state", "hand.db")); !os.IsNotExist(err) {
		t.Fatalf("selected identity failure mutated Fleet target: %v", err)
	}
}

func buildReleaseFixtureHand(t *testing.T, version, channel, commit, distribution string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hand")
	ldflags := fmt.Sprintf("-s -w -X main.version=%s -X main.channel=%s -X main.commit=%s -X main.distribution=%s", version, channel, commit, distribution)
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", path, ".")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build release fixture: %v: %s", err, out)
	}
	return path
}

func TestReleaseWorkflowPublishesOneDraftAssetSetFromExactCheckout(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Jobs map[string]workflowJobDef `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &document); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	release := document.Jobs["release-please"]
	if release.Outputs["sha"] != "${{ steps.release.outputs.sha }}" {
		t.Fatalf("release-please sha output = %q, want exact release SHA", release.Outputs["sha"])
	}
	configData, err := os.ReadFile(filepath.Join(repoRoot(t), "release-please-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Draft            bool `json:"draft"`
		ForceTagCreation bool `json:"force-tag-creation"`
	}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatalf("parse release-please config: %v", err)
	}
	if !config.Draft || !config.ForceTagCreation {
		t.Fatalf("release-please config = %#v, want draft and forced tag creation", config)
	}

	build := document.Jobs["build"]
	if got, want := workflowValue(t, build.Steps, "checkout", "ref"), "${{ needs.release-please.outputs.sha }}"; got != want {
		t.Fatalf("build checkout ref = %q, want %q", got, want)
	}
	executeWorkflowBuild(t, workflowStep(t, build.Steps, "Build (unix)"), "linux", "amd64")
	executeWorkflowBuild(t, workflowStep(t, build.Steps, "Build (windows)"), "windows", "amd64")

	publish := document.Jobs["publish"]
	if got, want := workflowValue(t, publish.Steps, "checkout", "ref"), "${{ needs.release-please.outputs.sha }}"; got != want {
		t.Fatalf("publish checkout ref = %q, want %q", got, want)
	}
	upload := workflowStep(t, publish.Steps, "Upload to release")
	if upload.With["draft"] != true || upload.With["fail_on_unmatched_files"] != true {
		t.Fatalf("release upload inputs = %#v, want a complete draft upload", upload.With)
	}
	executeWorkflowGhStep(t, workflowStep(t, publish.Steps, "Verify complete draft before publication"), "view")
	executeWorkflowGhStep(t, workflowStep(t, publish.Steps, "Publish complete release"), "edit")
}

func workflowStep(t *testing.T, steps []workflowStepDef, name string) workflowStepDef {
	t.Helper()
	for _, step := range steps {
		if name == "" && step.Uses != "" {
			return step
		}
		if name == "checkout" && strings.Contains(step.Uses, "actions/checkout@") {
			return step
		}
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("workflow step %q not found", name)
	return workflowStepDef{}
}

func workflowValue(t *testing.T, steps []workflowStepDef, stepName, key string) string {
	t.Helper()
	step := workflowStep(t, steps, stepName)
	value, ok := step.With[key].(string)
	if !ok {
		t.Fatalf("workflow step %q value %q = %#v, want string", stepName, key, step.With[key])
	}
	return value
}

func executeWorkflowBuild(t *testing.T, step workflowStepDef, goos, goarch string) {
	t.Helper()
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "tool.log")
	if runtime.GOOS != "windows" {
		writeExecutable(t, filepath.Join(fakeBin, "git"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+logPath+"'\n[ \"$1\" = rev-parse ] && [ \"$2\" = HEAD ] && printf '%s\\n' '"+releaseCommit+"'\n")
		writeExecutable(t, filepath.Join(fakeBin, "go"), "#!/bin/sh\nset -eu\nprintf 'go %s\\n' \"$*\" >> '"+logPath+"'\ncase \"$1 $2\" in\n  'env GOHOSTOS') printf 'linux\\n' ;;\n  'env GOHOSTARCH') printf 'amd64\\n' ;;\n  build*) out=hand; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -o ]; then out=$2; shift 2; else shift; fi; done; cat > \"$out\" <<'EOF'\n#!/bin/sh\nprintf '%s\\n' 'version: 1.2.3' 'channel: stable' 'commit: "+releaseCommit+"' 'distribution: github'\nEOF\nchmod 755 \"$out\" ;;\n  *) exit 1 ;;\nesac\n")
		writeExecutable(t, filepath.Join(fakeBin, "tar"), "#!/bin/sh\nfor arg do case \"$arg\" in *.tar.gz) : > \"$arg\" ;; esac; done\n")
		writeExecutable(t, filepath.Join(fakeBin, "zip"), "#!/bin/sh\n: > \"$1\"\n")
	}
	run := substituteWorkflowExpressions(step.Run, goos, goarch)
	if runtime.GOOS == "windows" {
		run = windowsWorkflowFixtureFunctions() + run
	}
	cmd := exec.Command("sh", "-eu", "-c", run)
	cmd.Dir = t.TempDir()
	extra := []string{"GOOS=" + goos, "GOARCH=" + goarch}
	if runtime.GOOS == "windows" {
		extra = append(extra, "HAND_WORKFLOW_LOG="+filepath.ToSlash(logPath))
	}
	cmd.Env = envWithPath(fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), extra...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("workflow build step failed: %v: %s", err, out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "main.commit="+releaseCommit) {
		t.Fatalf("workflow build did not pass the release commit to go: %q", log)
	}
}

func windowsWorkflowFixtureFunctions() string {
	return "git() {\n" +
		"  printf '%s\\n' \"$*\" >> \"$HAND_WORKFLOW_LOG\"\n" +
		"  [ \"$1\" = rev-parse ] && [ \"$2\" = HEAD ] && printf '%s\\n' '" + releaseCommit + "'\n" +
		"}\n" +
		"go() {\n" +
		"  printf 'go %s\\n' \"$*\" >> \"$HAND_WORKFLOW_LOG\"\n" +
		"  case \"$1 $2\" in\n" +
		"    'env GOHOSTOS') printf 'windows\\n' ;;\n" +
		"    'env GOHOSTARCH') printf 'amd64\\n' ;;\n" +
		"    build*) out=hand; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -o ]; then out=$2; shift 2; else shift; fi; done; : > \"$out\" ;;\n" +
		"    *) return 1 ;;\n" +
		"  esac\n" +
		"}\n" +
		"tar() {\n" +
		"  for arg do case \"$arg\" in *.tar.gz) : > \"$arg\" ;; esac; done\n" +
		"}\n" +
		"zip() { : > \"$1\"; }\n"
}

func executeWorkflowGhStep(t *testing.T, step workflowStepDef, action string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "gh"), "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> '"+logPath+"'\ncase \"$*\" in\n  *'--jq .isDraft'*) printf 'true\\n' ;;\n  *' --json assets '*) printf 'hand-linux-amd64.tar.gz\\nhand-linux-arm64.tar.gz\\nhand-darwin-amd64.tar.gz\\nhand-darwin-arm64.tar.gz\\nhand-windows-amd64.zip\\nbootstrap.sh\\nbootstrap.ps1\\nchecksums.txt\\n' ;;\n  *'release edit'*) : ;;\n  *) exit 1 ;;\nesac\n")
	cmd := exec.Command("sh", "-eu", "-c", substituteWorkflowExpressions(step.Run, "", ""))
	cmd.Env = envWithPath(fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "RELEASE_TAG=v1.2.3", "GH_TOKEN=test", "GITHUB_REPOSITORY=atqamz/hand")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("workflow gh %s step failed: %v: %s", action, err, out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "release "+action) {
		t.Fatalf("workflow gh %s step did not invoke gh release %s: %q", action, action, log)
	}
}

func substituteWorkflowExpressions(run, goos, goarch string) string {
	return strings.NewReplacer(
		"${{ needs.release-please.outputs.sha }}", releaseCommit,
		"${{ needs.release-please.outputs.version }}", "1.2.3",
		"${{ matrix.goos }}", goos,
		"${{ matrix.goarch }}", goarch,
	).Replace(run)
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func unixBootstrapAsset(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix bootstrap behavior is covered on Unix runners")
	}
	return fmt.Sprintf("hand-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

func writePlaceholderReleaseAssets(t *testing.T, output, selected string) {
	t.Helper()
	for _, name := range []string{
		"hand-linux-amd64.tar.gz",
		"hand-linux-arm64.tar.gz",
		"hand-darwin-amd64.tar.gz",
		"hand-darwin-arm64.tar.gz",
		"hand-windows-amd64.zip",
	} {
		if name == selected {
			continue
		}
		if err := os.WriteFile(filepath.Join(output, name), []byte("fixture "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func envWithPath(path string, extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra)+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "PATH") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, append([]string{"PATH=" + path}, extra...)...)
	return env
}

func runPrepareRelease(t *testing.T, output, tag, version, commit string) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), ".github", "scripts", "prepare-release.sh"), tag, version, commit, filepath.ToSlash(output))
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare-release.sh: %v: %s", err, out)
	}
}

func checksumLine(t *testing.T, path, name string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]) + "  " + name
}

func writeTarGzHand(t *testing.T, archivePath, handPath string) {
	t.Helper()
	data, err := os.ReadFile(handPath)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "hand", Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
