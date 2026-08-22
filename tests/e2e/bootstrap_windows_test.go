//go:build e2e && windows

package e2e

import (
	"archive/zip"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Resolved once, relative to this package, so a test never depends on the working directory
// `go test` happened to be invoked from.
var bootstrapPS1Script = func() string {
	abs, err := filepath.Abs(filepath.Join("..", "..", "bootstrap.ps1"))
	if err != nil {
		panic(err)
	}
	return abs
}()

// exec.Command resolves a bare name against this process's own PATH at call time, which is not
// guaranteed to carry either PowerShell edition on every runner, so this also checks each
// edition's fixed, well-known install location before giving up.
func findPowerShell() (string, error) {
	names := []string{"powershell.exe", "pwsh.exe"}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	candidates := make([]string, 0, 4)
	if root := os.Getenv("SystemRoot"); root != "" {
		candidates = append(candidates, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	}
	for _, root := range []string{os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, "PowerShell", "7", "pwsh.exe"))
		}
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", errors.New("PowerShell was not found on PATH or in its standard Windows install locations")
}

// A hand-picked minimal allowlist is the wrong isolation strategy on Windows: PATHEXT,
// LOCALAPPDATA and TEMP all matter to bootstrap.ps1, so this starts from the real environment
// and overrides only USERPROFILE and the app-data variables read off of it, redirected under home.
func windowsIsolatedEnv(home string, extraEnv []string) []string {
	overridden := map[string]string{
		"USERPROFILE":  home,
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		"APPDATA":      filepath.Join(home, "AppData", "Roaming"),
		"TEMP":         filepath.Join(home, "Temp"),
		"TMP":          filepath.Join(home, "Temp"),
	}
	for _, entry := range extraEnv {
		if name, value, ok := strings.Cut(entry, "="); ok {
			overridden[strings.ToUpper(name)] = value
		}
	}
	env := make([]string, 0, len(os.Environ())+len(overridden))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overridden[strings.ToUpper(name)]; !replaced {
			env = append(env, entry)
		}
	}
	for name, value := range overridden {
		env = append(env, name+"="+value)
	}
	return env
}

// Runs bootstrap.ps1 under native Windows PowerShell against an environment isolated to home,
// so a test can prove nothing beyond that isolated environment ever reaches the script.
func runBootstrapPS1(t *testing.T, home string, extraEnv []string, args ...string) invocation {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "hand-windows-amd64.zip")
	writeHandZip(t, archive)
	return runBootstrapPS1WithArchives(t, home, archive, archive, extraEnv, args...)
}

func runBootstrapPS1WithArchives(t *testing.T, home, expectedArchive, downloadedArchive string, extraEnv []string, args ...string) invocation {
	t.Helper()
	seedPrivateRuntime(t, home)
	for _, dir := range []string{filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming"), filepath.Join(home, "Temp")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := boundBootstrapWithDigest(t, bootstrapPS1Script, sha256Path(t, expectedArchive))
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-Fleet":
			if index+1 >= len(args) {
				t.Fatal("-Fleet has no test value")
			}
			fleet := strings.ReplaceAll(args[index+1], "'", "''")
			script = strings.Replace(script, `[string]$Fleet = (Join-Path $env:USERPROFILE "secondhand-fleet"),`, `[string]$Fleet = '`+fleet+`',`, 1)
			index++
		case "-Check":
			script = strings.Replace(script, "[switch]$Check", "[switch]$Check = $true", 1)
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	command := "$archivePath = $env:HAND_TEST_ARCHIVE; " +
		"function Invoke-WebRequest { param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile); " +
		"if ($Uri -notlike '*hand-windows-amd64.zip') { throw \"unexpected test download: $Uri\" }; " +
		"Copy-Item -LiteralPath $archivePath -Destination $OutFile -Force }; " +
		"$script = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encoded + "')); " +
		"Invoke-Expression $script"
	powershell, err := findPowerShell()
	if err != nil {
		t.Fatalf("find a native PowerShell executable: %v", err)
	}
	psArgs := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command}
	cmd := exec.Command(powershell, psArgs...)
	extraEnv = append(extraEnv, "SECONDHAND_HOME="+filepath.Join(home, ".secondhand"), "HAND_TEST_ARCHIVE="+downloadedArchive)
	cmd.Env = windowsIsolatedEnv(home, extraEnv)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run bootstrap.ps1 %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	t.Logf("$ bootstrap.ps1 %s\n  exit %d\n  stdout: %s\n  stderr: %s",
		strings.Join(args, " "), code, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
	return invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestBootstrapPS1ExecutesThroughIEXWithoutAFilePath(t *testing.T) {
	dir := binDir(t)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")
	installFakeCmdExe(t, dir, "claude")

	home := t.TempDir()
	fleetLeaf := "secondhand fleet 日本"
	got := runBootstrapPS1(t, home, nil, "-Fleet", filepath.Join(home, fleetLeaf))
	fleet := filepath.Join(home, fleetLeaf)
	if got.code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", got.code, got.stdout, got.stderr)
	}
	if !isFleetHomeWindows(fleet) {
		t.Fatalf("%s was not initialized", fleet)
	}
	if _, err := os.Stat(filepath.Join(home, "AppData", "Local", "hand", "hand.exe")); err != nil {
		t.Fatalf("fresh IEX adoption did not install hand.exe: %v", err)
	}
	if !strings.Contains(got.stdout, "ready: true") {
		t.Fatalf("stdout = %q, want a ready fleet", got.stdout)
	}
}

func TestBootstrapPS1RejectsArchiveDigestMismatchBeforeExtraction(t *testing.T) {
	home := t.TempDir()
	expectedArchive := filepath.Join(t.TempDir(), "expected-hand-windows-amd64.zip")
	writeHandZip(t, expectedArchive)
	downloadedArchive := filepath.Join(t.TempDir(), "wrong-hand-windows-amd64.zip")
	if err := os.WriteFile(downloadedArchive, []byte("wrong archive"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runBootstrapPS1WithArchives(t, home, expectedArchive, downloadedArchive, nil)
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "digest mismatch") {
		t.Fatalf("stdout = %q, want the archive digest refusal", got.stdout)
	}
	if _, err := os.Stat(filepath.Join(home, "AppData", "Local", "hand", "hand.exe")); !os.IsNotExist(err) {
		t.Fatalf("digest failure selected a Hand executable: %v", err)
	}
}

func buildWindowsHandWithDistribution(t *testing.T, path, distribution string) {
	t.Helper()
	ldflags := fmt.Sprintf("-X main.version=1.2.3 -X main.channel=stable -X main.commit=0123456789abcdef0123456789abcdef01234567 -X main.distribution=%s", distribution)
	cmd := exec.Command(goBin, "build", "-tags", "e2e,test", "-ldflags", ldflags, "-o", path, ".")
	cmd.Dir = filepath.Join("..", "..")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s Hand fixture: %v\n%s", distribution, err, output)
	}
}

func TestBootstrapPS1DoesNotOverwritePackageOwnedHandOnFreshAdoption(t *testing.T) {
	dir := binDir(t)
	packageHand := filepath.Join(dir, "hand.exe")
	buildWindowsHandWithDistribution(t, packageHand, "brew")
	before, err := os.ReadFile(packageHand)
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	got := runBootstrapPS1(t, home, nil)
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "will not replace a brew build") {
		t.Fatalf("stdout = %q, want package ownership refusal", got.stdout)
	}
	after, err := os.ReadFile(packageHand)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("package-owned Hand was overwritten")
	}
	if _, err := os.Stat(filepath.Join(home, "AppData", "Local", "hand", "hand.exe")); !os.IsNotExist(err) {
		t.Fatalf("bootstrap shadowed package-owned Hand with a direct install: %v", err)
	}
}

// Puts a copy of the already-built hand binary on dir as hand.exe, the extension native Windows
// PATH resolution (and a real install.ps1) requires; a bare "hand" is never found by Get-Command.
func installFakeHandExe(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(handBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hand.exe"), data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeHandZip(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(handBin)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(file)
	entry, err := zipWriter.Create("hand.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// Drops a no-op .cmd on dir under name, standing in for an installed tool or an installed,
// authenticated coding-agent harness: bootstrap.ps1 only ever needs Get-Command to find it.
func installFakeCmdExe(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".cmd"), []byte("@exit /b 0\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func isFleetHomeWindows(fleet string) bool {
	_, err := os.Stat(filepath.Join(fleet, "state", "hand.db"))
	return err == nil
}

func TestBootstrapPS1HappyPathReconcilesFleetAndPrintsTheInstalledHarness(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")
	installFakeCmdExe(t, dir, "claude")

	home := t.TempDir()
	fleet := filepath.Join(home, "secondhand-fleet")

	got := runBootstrapPS1(t, home, nil, "-Fleet", fleet, "-Yes")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if !isFleetHomeWindows(fleet) {
		t.Fatalf("%s was not initialized as a fleet home", fleet)
	}
	for _, want := range []string{"claude,true", "ready: true"} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("stdout = %q, want it to contain %q", got.stdout, want)
		}
	}
}

func TestBootstrapPS1RefusesAForeignNonEmptyFleetTarget(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")

	home := t.TempDir()
	fleet := t.TempDir()
	if err := os.WriteFile(filepath.Join(fleet, "unrelated.txt"), []byte("not a fleet\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runBootstrapPS1(t, home, nil, "-Fleet", fleet, "-Yes")
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "refusing to adopt it") {
		t.Fatalf("stdout = %q, want a refusal for the foreign non-empty target", got.stdout)
	}
	if isFleetHomeWindows(fleet) {
		t.Fatal("bootstrap.ps1 mutated a foreign non-empty target instead of refusing it")
	}
}

func TestBootstrapPS1CheckModeNeverMutatesAnAbsentFleetTarget(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")
	installFakeCmdExe(t, dir, "claude")

	home := t.TempDir()
	fleet := filepath.Join(home, "secondhand-fleet")

	got := runBootstrapPS1(t, home, nil, "-Fleet", fleet, "-Check")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 for check mode (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(fleet); !os.IsNotExist(err) {
		t.Fatalf("-Check created %s; check mode must never mutate", fleet)
	}
}

func TestBootstrapPS1UsesPrivateRuntimeWithoutCoreToolsOnPath(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "claude")

	home := t.TempDir()
	fleet := filepath.Join(home, "secondhand-fleet")

	got := runBootstrapPS1(t, home, nil, "-Fleet", fleet)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	for _, notWant := range []string{"declining to install", "installing treehouse", "installing herdr", "installing git"} {
		if strings.Contains(got.stdout, notWant) {
			t.Fatalf("stdout = %q, must not contain legacy PATH installation %q", got.stdout, notWant)
		}
	}
	if !strings.Contains(got.stdout, "ready: true") {
		t.Fatalf("stdout = %q, want private runtime readiness", got.stdout)
	}
}

func TestBootstrapPS1NeverInstallsAHarnessOrNoMistakesAutomatically(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")
	// No coding-agent harness and no no-mistakes on PATH at all.

	home := t.TempDir()
	fleet := filepath.Join(home, "secondhand-fleet")

	got := runBootstrapPS1(t, home, nil, "-Fleet", fleet, "-Yes")
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1: no harness is installed, so the fleet cannot be ready (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "- harness") {
		t.Fatalf("stdout = %q, want the missing harness reported as blocking", got.stdout)
	}
	for _, name := range []string{"claude", "codex", "grok", "pi", "opencode", "no-mistakes"} {
		if strings.Contains(got.stdout, "installing "+name) {
			t.Fatalf("stdout = %q, want bootstrap to never attempt installing a harness or no-mistakes", got.stdout)
		}
		if _, err := os.Stat(filepath.Join(dir, name+".cmd")); err == nil {
			t.Fatalf("%s was created on PATH; bootstrap must only ever detect harnesses, never install one", name)
		}
	}
}

func TestBootstrapPS1AcceptsAFleetTargetPathContainingSpaces(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")
	installFakeCmdExe(t, dir, "codex")

	home := t.TempDir()
	fleet := filepath.Join(home, "my secondhand fleet")

	got := runBootstrapPS1(t, home, nil, "-Fleet", fleet, "-Yes")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 for a fleet path containing spaces (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if !isFleetHomeWindows(fleet) {
		t.Fatalf("%s was not initialized", fleet)
	}
}

func TestBootstrapPS1NeverForwardsAmbientSecretsIntoItsOutput(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")
	installFakeCmdExe(t, dir, "claude")

	home := t.TempDir()
	fleet := filepath.Join(home, "secondhand-fleet")
	const secret = "poison-token-do-not-print-1x2y3z"

	got := runBootstrapPS1(t, home, []string{"GH_TOKEN=" + secret, "ANTHROPIC_API_KEY=" + secret}, "-Fleet", fleet, "-Yes")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if strings.Contains(got.stdout, secret) || strings.Contains(got.stderr, secret) {
		t.Fatal("bootstrap.ps1 leaked an ambient credential into its own output")
	}
}

func TestBootstrapPS1ReconcilesAnExistingFleetIdempotently(t *testing.T) {
	dir := binDir(t)
	installFakeHandExe(t, dir)
	installFakeCmdExe(t, dir, "treehouse")
	installFakeCmdExe(t, dir, "herdr")
	installFakeCmdExe(t, dir, "claude")

	home := t.TempDir()
	fleet := filepath.Join(home, "secondhand-fleet")

	first := runBootstrapPS1(t, home, nil, "-Fleet", fleet, "-Yes")
	if first.code != 0 {
		t.Fatalf("first run: exit = %d, want 0 (stdout %q, stderr %q)", first.code, first.stdout, first.stderr)
	}

	second := runBootstrapPS1(t, home, nil, "-Fleet", fleet, "-Yes")
	if second.code != 0 {
		t.Fatalf("second run: exit = %d, want 0 (stdout %q, stderr %q)", second.code, second.stdout, second.stderr)
	}
	if !strings.Contains(second.stdout, "agents_md: unchanged") {
		t.Fatalf("second run stdout = %q, want hand init to report the already-canonical AGENTS.md unchanged", second.stdout)
	}
	if !strings.Contains(second.stdout, "ready: true") {
		t.Fatalf("second run stdout = %q, want a ready fleet on a repeat run", second.stdout)
	}
}
