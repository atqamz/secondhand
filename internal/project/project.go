// Package project manages the registry of git projects: a table in hand's
// sqlite database, with data/projects.md kept in step as the human-readable
// projection. The database remains authoritative if the two disagree.
package project

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/atqamz/secondhand/internal/atomicfile"
	"github.com/atqamz/secondhand/internal/ghutil"
	"github.com/atqamz/secondhand/internal/store"
)

const (
	ModeNoMistakes = "no-mistakes"
	ModeDirectPR   = "direct-pr"
	ModeLocalOnly  = "local-only"
)

// ErrNotFound is wrapped into the error Remove returns when name isn't
// registered, rendering as `project "<name>" not registered` to match the
// wording the cmd layer uses for the same condition.
var ErrNotFound = errors.New("not registered")

type Project = store.Project

func RegistryPath(homeDir string) string {
	return homeDir + "/data/projects.md"
}

// DeriveName extracts a project name from a git URL: the last path segment minus ".git".
func DeriveName(url string) string {
	name := url
	if idx := strings.LastIndexAny(name, "/:"); idx != -1 {
		name = name[idx+1:]
	}
	name = strings.TrimSuffix(name, ".git")
	return name
}

func List(homeDir string) ([]Project, error) {
	db, err := openRegistry(homeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListProjects()
}

func ListReadOnly(homeDir string) ([]Project, error) {
	db, err := store.OpenReadOnly(homeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListProjects()
}

// data/projects.md survives its own import as the projection, so its absence
// cannot serve as the done marker the way a task's JSON file does.
const legacyRegistryKey = "projects.md"

func openRegistry(homeDir string) (*store.DB, error) {
	db, err := store.Open(homeDir)
	if err != nil {
		return nil, err
	}
	if err := importLegacyRegistry(db, homeDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func importLegacyRegistry(db *store.DB, homeDir string) error {
	done, err := db.Migrated(legacyRegistryKey)
	if err != nil || done {
		return err
	}
	// The check, the inserts and the mark are separate statements over a file
	// sqlite cannot see, so unlocked they can re-insert a project another
	// process removed after importing it. Same lock as the task import.
	unlock, err := store.Lock(homeDir, store.MigrationLock, false)
	if err != nil {
		return fmt.Errorf("lock migration: %w", err)
	}
	defer unlock()

	if done, err := db.Migrated(legacyRegistryKey); err != nil || done {
		return err
	}
	projects, err := parseRegistryFile(homeDir)
	if err != nil {
		return err
	}
	for _, p := range projects {
		// A name listed twice was already unreachable past the first match, so
		// dropping the later one imports exactly what the file resolved to.
		if err := db.AddProject(p); err != nil && !errors.Is(err, store.ErrProjectExists) {
			return err
		}
	}
	return db.MarkMigrated(legacyRegistryKey)
}

// Reading the file without going through the database is what keeps importing
// an existing fleet independent of the binary that wrote it.
func parseRegistryFile(homeDir string) ([]Project, error) {
	f, err := os.Open(RegistryPath(homeDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project registry: %w", err)
	}
	defer func() { _ = f.Close() }()

	var projects []Project
	scanner := bufio.NewScanner(f)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, ok := parseLine(line)
		if !ok {
			return nil, fmt.Errorf("invalid project registry line %d", lineNumber)
		}
		projects = append(projects, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read project registry: %w", err)
	}
	return projects, nil
}

func parseLine(line string) (Project, bool) {
	if !strings.HasPrefix(line, "- ") {
		return Project{}, false
	}
	line = strings.TrimPrefix(line, "- ")

	nameRest := strings.SplitN(line, ":", 2)
	if len(nameRest) != 2 {
		return Project{}, false
	}
	name := strings.TrimSpace(nameRest[0])
	rest := strings.TrimSpace(nameRest[1])

	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return Project{}, false
	}
	url := fields[0]
	mode := ""
	upstream := ""
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "mode=") {
			mode = strings.TrimPrefix(f, "mode=")
		}
		if strings.HasPrefix(f, "upstream=") {
			upstream = strings.TrimPrefix(f, "upstream=")
		}
	}
	if name == "" || url == "" || mode == "" {
		return Project{}, false
	}
	if !validMode(mode) {
		return Project{}, false
	}
	if upstream != "" {
		slug, ok := ParseRepoRef(upstream)
		if !ok {
			return Project{}, false
		}
		upstream = slug
	}
	return Project{Name: name, URL: url, Mode: mode, Upstream: upstream}, true
}

// ParseRepoRef normalizes a repo reference an operator types - a bare "owner/repo" or any
// remote URL form ghutil understands - into the slug the PR guard compares against. It refuses
// rather than guessing: an upstream nobody can resolve to a slug would widen that guard.
func ParseRepoRef(ref string) (string, bool) {
	// Whitespace is refused outright rather than stored: the registry projection writes the
	// slug as a whitespace-separated upstream=<slug> field, which parseLine reads back
	// truncated and then rejects, breaking every project command against a rebuilt db.
	if strings.ContainsAny(ref, " \t\r\n") {
		return "", false
	}
	if slug, ok := ghutil.RepoSlugFromRemote(ref); ok {
		return slug, true
	}
	owner, repo, ok := strings.Cut(strings.TrimSuffix(ref, ".git"), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", false
	}
	return owner + "/" + repo, true
}

// SetUpstream declares which repo a fork project opens its PRs against, or
// clears the declaration when upstream is empty.
func SetUpstream(homeDir, name, upstream string) error {
	unlock, err := lockRegistry(homeDir)
	if err != nil {
		return err
	}
	defer unlock()

	db, err := openRegistry(homeDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	updated, err := db.SetProjectUpstream(name, upstream)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("project %q %w", name, ErrNotFound)
	}
	return writeProjection(db, homeDir)
}

// Find returns the project with the given name, or false if not registered.
func Find(homeDir, name string) (Project, bool, error) {
	projects, err := List(homeDir)
	if err != nil {
		return Project{}, false, err
	}
	for _, p := range projects {
		if p.Name == name {
			return p, true, nil
		}
	}
	return Project{}, false, nil
}

// GateState is the result of asking the no-mistakes binary whether a repo's gate is initialized.
type GateState int

const (
	GateReady GateState = iota
	GateNotInitialized
)

const gateNotInitializedMarker = "repo not initialized"

// What `no-mistakes status` prints, exiting 0, when clonePath exists but isn't a git repository
// at all - a different, unrepairable outcome from GateNotInitialized: `no-mistakes init` fixes
// a repo that was never initialized, not a directory that isn't a git repo.
const notGitRepoMarker = "not in a git repository"

// GateInitCommand is the exact remedy for GateNotInitialized. no-mistakes init is idempotent and
// repairs a stale working_path in place, so callers should print this verbatim rather than describe it.
func GateInitCommand(clonePath string) string {
	return fmt.Sprintf("cd %s && no-mistakes init", clonePath)
}

// GateStatus asks the no-mistakes binary whether clonePath's gate is initialized, rather than
// reading ~/.no-mistakes/state.sqlite, which is another tool's private schema. The outcome comes
// from the output text: no-mistakes status exits 0 whether or not the repo is initialized.
func GateStatus(clonePath string) (GateState, error) {
	// A clone path missing from disk and one that is not a git repository are both plain errors
	// naming the real cause, never GateReady, which would let a caller dispatch into a project
	// the gate cannot cover.
	if _, err := os.Stat(clonePath); err != nil {
		return GateReady, fmt.Errorf("no-mistakes clone path: %w", err)
	}
	cmd := exec.Command("no-mistakes", "status")
	cmd.Dir = clonePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Distinct from GateNotInitialized, because the remedy for a binary that is missing,
		// unexecutable, or failing unexpectedly is not `no-mistakes init`.
		return GateReady, fmt.Errorf("no-mistakes binary not found or not runnable: %w", err)
	}
	text := string(out)
	if strings.Contains(text, gateNotInitializedMarker) {
		return GateNotInitialized, nil
	}
	if strings.Contains(text, notGitRepoMarker) {
		return GateReady, fmt.Errorf("no-mistakes clone path is not a git repository: %s", clonePath)
	}
	return GateReady, nil
}

func Add(homeDir string, p Project) error {
	if !validMode(p.Mode) {
		return fmt.Errorf("invalid project mode %q", p.Mode)
	}
	unlock, err := lockRegistry(homeDir)
	if err != nil {
		return err
	}
	defer unlock()

	db, err := openRegistry(homeDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := db.AddProject(p); err != nil {
		return err
	}
	return writeProjection(db, homeDir)
}

func validMode(mode string) bool {
	return mode == ModeNoMistakes || mode == ModeDirectPR || mode == ModeLocalOnly
}

func Remove(homeDir, name string) error {
	unlock, err := lockRegistry(homeDir)
	if err != nil {
		return err
	}
	defer unlock()

	db, err := openRegistry(homeDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	removed, err := db.RemoveProject(name)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("project %q %w", name, ErrNotFound)
	}
	return writeProjection(db, homeDir)
}

// sqlite serializes the row write, but the projection is a whole read-modify-
// write over a file: without this, two concurrent adds can each render from
// their own snapshot and the later write drops the other's line.
func lockRegistry(homeDir string) (func(), error) {
	lock, err := os.OpenFile(RegistryPath(homeDir)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock project registry: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock project registry: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

// Each project line is rewritten in place rather than regrouped: the live file
// interleaves hand-written `# profile=` comments with the entries they
// describe, and moving entries would rebind a comment to the wrong repo.
func writeProjection(db *store.DB, homeDir string) error {
	projects, err := db.ListProjects()
	if err != nil {
		return err
	}
	registered := make(map[string]Project, len(projects))
	for _, p := range projects {
		registered[p.Name] = p
	}

	existing, err := os.ReadFile(RegistryPath(homeDir))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read project registry: %w", err)
	}

	var rendered []string
	placed := make(map[string]bool, len(projects))
	for _, line := range strings.Split(strings.TrimSuffix(string(existing), "\n"), "\n") {
		p, isProject := parseLine(strings.TrimSpace(line))
		if !isProject {
			rendered = append(rendered, line)
			continue
		}
		current, ok := registered[p.Name]
		if !ok || placed[p.Name] {
			continue
		}
		placed[p.Name] = true
		rendered = append(rendered, renderProjectLine(current))
	}

	rendered = trimTrailingBlanks(rendered)
	for _, p := range projects {
		if !placed[p.Name] {
			rendered = append(rendered, renderProjectLine(p))
		}
	}

	content := strings.Join(rendered, "\n")
	if err := atomicfile.Write(RegistryPath(homeDir), ".projects.md-", []byte(content+"\n"), 0o644); err != nil {
		return fmt.Errorf("write project registry: %w", err)
	}
	return nil
}

func renderProjectLine(p Project) string {
	line := fmt.Sprintf("- %s: %s mode=%s", p.Name, p.URL, p.Mode)
	if p.Upstream != "" {
		line += " upstream=" + p.Upstream
	}
	return line
}

func trimTrailingBlanks(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
