// Package store holds hand's machine state in a sqlite database at
// state/hand.db and owns the one-way migration from the state/<id>.json files
// that used to hold it. The prose corpus stays in files; see index.go.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/atqamz/hand/internal/shellquote"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	KindShip  = "ship"
	KindScout = "scout"
)

const (
	HoldKindOperator = "operator"
	HoldKindBlocked  = "blocked"
	// HoldKindLimit is the one machine-set kind: hand watch sets it when a worker's
	// harness stops on a usage limit and clears it when the worker runs again, so
	// `hand hold set` refuses it (atqamz/hand#136).
	HoldKindLimit = "limit"
)

// Wrapped by task readers, rendered by callers as `task "<id>" not found`.
var ErrTaskNotFound = errors.New("not found")

var ErrInvalidTransition = errors.New("invalid lifecycle transition")

var ErrLifecycleConflict = errors.New("lifecycle conflict")

var ErrOwnershipConflict = errors.New("attempt ownership conflict")

var ErrSendOwnershipConflict = errors.New("send ownership conflict")

var ErrSendInFlight = errors.New("send already in flight")

var ErrInvalidSendTransition = errors.New("invalid send transition")

// SQLITE_BUSY means the write lost the database lock rather than losing a lifecycle race, so the
// caller may retry it instead of being told a precondition it never violated failed.
var ErrContention = errors.New("database contention")

// Wrapped by ClearHold, rendered by callers as `hold "<id>" not found`.
var ErrHoldNotFound = errors.New("not found")

// Wrapped by AddProject so a caller importing a registry that already lists a
// name can tell a duplicate from a real write fault.
var ErrProjectExists = errors.New("already registered")

type Herdr struct {
	Session     string `json:"session"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	PaneID      string `json:"pane_id"`
}

type HerdrOwnership struct {
	AttemptID          int64            `json:"attempt_id"`
	TaskID             string           `json:"task_id"`
	Lifecycle          AttemptLifecycle `json:"lifecycle"`
	TeardownHerdrState string           `json:"teardown_herdr_state,omitempty"`
	Session            string           `json:"session"`
	WorkspaceID        string           `json:"workspace_id"`
	TabID              string           `json:"tab_id"`
	PaneID             string           `json:"pane_id"`
}

type TaskLifecycle string

const (
	TaskOpen     TaskLifecycle = "open"
	TaskTerminal TaskLifecycle = "terminal"
)

type AttemptLifecycle string

const (
	AttemptProvisioning AttemptLifecycle = "provisioning"
	AttemptRunning      AttemptLifecycle = "running"
	AttemptCompleted    AttemptLifecycle = "completed"
	AttemptFailed       AttemptLifecycle = "failed"
	AttemptInterrupted  AttemptLifecycle = "interrupted"
)

type SendState string

const (
	SendPending      SendState = "pending"
	SendNotSubmitted SendState = "not-submitted"
	SendSubmitted    SendState = "submitted"
	SendUncertain    SendState = "uncertain"
)

const (
	SendReasonTextRejectedBeforeAcceptance = "text-rejected-before-acceptance"
	SendReasonEnterRejectedAfterTextStaged = "enter-rejected-after-text-staged"
)

type SendOrigin string

const (
	SendOriginOperator          SendOrigin = "operator"
	SendOriginUsageLimitResume  SendOrigin = "usage-limit-resume"
	SendOriginLegacyUndelivered SendOrigin = "legacy-undelivered"
)

const (
	TeardownResourceReleasing               = "releasing"
	TeardownResourceReleased                = "released"
	TeardownResourceAmbiguous               = "ambiguous"
	TeardownResourceRetryable               = "retryable"
	TeardownResourceAbandoned               = "abandoned"
	TeardownCompletionPending               = "pending"
	TeardownCompletionAppended              = "appended"
	TeardownDispositionCompleted            = "completed"
	TeardownDispositionCompletedSafeDirt    = "completed-safe-dirt"
	TeardownDispositionForced               = "forced"
	TeardownDispositionNeverLaunched        = "never-launched"
	TeardownDispositionLaunchedProvisioning = "launched-provisioning"
	TeardownDispositionWorkerExitedUnlanded = "worker-exited-unlanded"
	TeardownDispositionProvisioningUnwound  = "provisioning-unwound"
	TeardownDispositionWorkerNeverStarted   = "worker-never-started"
)

type Task struct {
	ID               string        `json:"id"`
	Project          string        `json:"project"`
	Kind             string        `json:"kind"`
	Brief            string        `json:"brief"`
	Lifecycle        TaskLifecycle `json:"lifecycle"`
	ActiveAttemptID  int64         `json:"active_attempt_id"`
	PR               string        `json:"pr"`
	MergeExecuted    bool          `json:"merged"`
	MergeExecutedAt  string        `json:"merged_at"`
	ReportOffset     int64         `json:"report_offset"`
	ReportDigest     string        `json:"report_digest"`
	MergeAnnounced   bool          `json:"pr_merged_observed"`
	DeliveredAt      string        `json:"delivered_at"`
	DeliveredReason  string        `json:"delivered_reason"`
	CreatedAt        string        `json:"created_at"`
	RepairCode       string        `json:"repair_code"`
	RepairReason     string        `json:"repair_reason"`
	RepairAttemptID  int64         `json:"repair_attempt_id"`
	RepairObservedAt string        `json:"repair_observed_at"`
	// A supervisor's own act, distinct from report_offset/report_digest which record what a watcher has
	// announced: atqamz/hand#267 keeps the two markers apart because they answer different questions.
	AcknowledgedAt     string `json:"acknowledged_at"`
	AcknowledgedReason string `json:"acknowledged_reason"`
	AcknowledgedOffset int64  `json:"acknowledged_offset"`
	AcknowledgedDigest string `json:"acknowledged_digest"`
}

type Attempt struct {
	ID                      int64            `json:"id"`
	TaskID                  string           `json:"task_id"`
	Ordinal                 int              `json:"ordinal"`
	Lifecycle               AttemptLifecycle `json:"lifecycle"`
	Harness                 string           `json:"harness"`
	Model                   string           `json:"model"`
	Effort                  string           `json:"effort"`
	ExecutionClass          string           `json:"execution_class"`
	PlannedAgainst          string           `json:"planned_against"`
	RequestedProfile        string           `json:"requested_profile"`
	RoutingSource           string           `json:"routing_source"`
	Worktree                string           `json:"worktree"`
	Branch                  string           `json:"branch"`
	LeaseID                 string           `json:"lease_id"`
	Herdr                   Herdr            `json:"herdr"`
	CreatedAt               string           `json:"created_at"`
	PaneStartedAt           string           `json:"pane_started_at"`
	LaunchSubmittedAt       string           `json:"launch_submitted_at"`
	LaunchConfirmedAt       string           `json:"launch_confirmed_at"`
	StatusChangedAt         string           `json:"status_changed_at"`
	StatusChangedFor        string           `json:"status_changed_for"`
	DoneVerified            bool             `json:"done_verified"`
	LastReportState         string           `json:"last_report_state"`
	LastReportNote          string           `json:"last_report_note"`
	SendUndeliveredMessage  string           `json:"send_undelivered_message"`
	SendUndeliveredAt       string           `json:"send_undelivered_at"`
	ParkedFiredFor          string           `json:"parked_fired_for"`
	UsageLimitRetryAt       string           `json:"usage_limit_retry_at"`
	UsageLimitAttempts      int              `json:"usage_limit_attempts"`
	TeardownTerminalAttempt AttemptLifecycle `json:"teardown_terminal_attempt,omitempty"`
	TeardownDisposition     string           `json:"teardown_disposition,omitempty"`
	TeardownHerdrState      string           `json:"teardown_herdr_state,omitempty"`
	TeardownWorktreeState   string           `json:"teardown_worktree_state,omitempty"`
	TeardownCompletionState string           `json:"teardown_completion_state,omitempty"`
	UsageLimitEpisode       int64            `json:"usage_limit_episode"`
	UsageLimitStuckEpisode  int64            `json:"usage_limit_stuck_episode"`
}

type SendAttempt struct {
	ID                int64      `json:"id"`
	TaskID            string     `json:"task_id"`
	AttemptID         int64      `json:"attempt_id"`
	Origin            SendOrigin `json:"origin"`
	Message           string     `json:"message"`
	State             SendState  `json:"state"`
	ReasonCode        string     `json:"reason_code"`
	CreatedAt         string     `json:"created_at"`
	FinalizedAt       string     `json:"finalized_at"`
	UsageLimitEpisode int64      `json:"usage_limit_episode,omitempty"`
}

type TaskHistory struct {
	Task          Task          `json:"task"`
	ActiveAttempt *Attempt      `json:"active_attempt,omitempty"`
	Attempts      []Attempt     `json:"attempts"`
	Sends         []SendAttempt `json:"sends,omitempty"`
}

func SendNeedsAttention(send SendAttempt) bool {
	if send.State == SendPending || send.State == SendUncertain {
		return true
	}
	return send.State == SendNotSubmitted && strings.HasPrefix(send.ReasonCode, SendReasonEnterRejectedAfterTextStaged)
}

func SendRetrySafe(send SendAttempt) bool {
	return send.State == SendNotSubmitted && strings.HasPrefix(send.ReasonCode, SendReasonTextRejectedBeforeAcceptance)
}

// Upstream is the "owner/repo" a fork project opens its PRs against, empty when it contributes to
// its own repo. A fork has two repos, the one URL names and the one its PRs live on, and only a
// declared upstream tells that pair from an unauthorized one (atqamz/hand#78).
type Project struct {
	Name     string
	URL      string
	Mode     string
	Upstream string
}

// Hold is its own row keyed by an arbitrary id, not a foreign key into task: it exists for a
// question left open by work hand teardown already terminalized, or by no task at all. BlockedOn
// carries the id a HoldKindBlocked hold waits on; Inferred marks a Reason scraped from a pane rather than observed directly.
type Hold struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	BlockedOn string `json:"blocked_on"`
	SetAt     string `json:"set_at"`
	Inferred  bool   `json:"inferred"`
}

// Every machine-state file lives here, database included, so a human looking
// for fleet state has one directory to look in.
func Dir(homeDir string) string {
	return filepath.Join(homeDir, "state")
}

func Path(homeDir string) string {
	return filepath.Join(Dir(homeDir), "hand.db")
}

type DB struct {
	sql   *sql.DB
	home  string
	empty bool
}

// The version-0 baseline plus every registered migration folded in: a column
// added here also needs its ALTER TABLE appended to migrations in
// schemaversion.go, or no database that already exists ever gains it.
const schema = `
CREATE TABLE IF NOT EXISTS task (
	id                TEXT PRIMARY KEY,
	project           TEXT NOT NULL DEFAULT '',
	kind              TEXT NOT NULL DEFAULT '',
	brief             TEXT NOT NULL DEFAULT '',
	lifecycle         TEXT NOT NULL DEFAULT 'open' CHECK (lifecycle IN ('open', 'terminal')),
	active_attempt_id INTEGER REFERENCES attempt(id),
	pr                TEXT NOT NULL DEFAULT '',
	merge_executed    INTEGER NOT NULL DEFAULT 0,
	merge_executed_at TEXT NOT NULL DEFAULT '',
	merge_announced   INTEGER NOT NULL DEFAULT 0,
	delivered_at      TEXT NOT NULL DEFAULT '',
	delivered_reason  TEXT NOT NULL DEFAULT '',
	report_offset     INTEGER NOT NULL DEFAULT 0,
	report_digest     TEXT NOT NULL DEFAULT '',
	created_at        TEXT NOT NULL DEFAULT '',
	repair_code       TEXT NOT NULL DEFAULT '',
	repair_reason     TEXT NOT NULL DEFAULT '',
	repair_attempt_id INTEGER NOT NULL DEFAULT 0,
	repair_observed_at TEXT NOT NULL DEFAULT '',
	acknowledged_at     TEXT NOT NULL DEFAULT '',
	acknowledged_reason TEXT NOT NULL DEFAULT '',
	acknowledged_offset INTEGER NOT NULL DEFAULT 0,
	acknowledged_digest TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS attempt (
	id                     INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id                TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
	ordinal                INTEGER NOT NULL,
	lifecycle              TEXT NOT NULL CHECK (lifecycle IN ('provisioning', 'running', 'completed', 'failed', 'interrupted')),
	harness                TEXT NOT NULL DEFAULT '',
	model                  TEXT NOT NULL DEFAULT '',
	effort                 TEXT NOT NULL DEFAULT '',
	execution_class        TEXT NOT NULL DEFAULT '',
	planned_against        TEXT NOT NULL DEFAULT '',
	requested_profile      TEXT NOT NULL DEFAULT '',
	routing_source        TEXT NOT NULL DEFAULT '',
	worktree               TEXT NOT NULL DEFAULT '',
	branch                 TEXT NOT NULL DEFAULT '',
	lease_id               TEXT NOT NULL DEFAULT '',
	herdr_session          TEXT NOT NULL DEFAULT '',
	herdr_workspace_id    TEXT NOT NULL DEFAULT '',
	herdr_tab_id           TEXT NOT NULL DEFAULT '',
	herdr_pane_id          TEXT NOT NULL DEFAULT '',
	created_at             TEXT NOT NULL DEFAULT '',
	pane_started_at        TEXT NOT NULL DEFAULT '',
	launch_submitted_at    TEXT NOT NULL DEFAULT '',
	launch_confirmed_at    TEXT NOT NULL DEFAULT '',
	status_changed_at      TEXT NOT NULL DEFAULT '',
	status_changed_for     TEXT NOT NULL DEFAULT '',
	done_verified          INTEGER NOT NULL DEFAULT 0,
	last_report_state      TEXT NOT NULL DEFAULT '',
	last_report_note       TEXT NOT NULL DEFAULT '',
	send_undelivered_message TEXT NOT NULL DEFAULT '',
	send_undelivered_at      TEXT NOT NULL DEFAULT '',
	parked_fired_for       TEXT NOT NULL DEFAULT '',
	usage_limit_retry_at   TEXT NOT NULL DEFAULT '',
	usage_limit_attempts   INTEGER NOT NULL DEFAULT 0,
	teardown_terminal_attempt TEXT NOT NULL DEFAULT '',
	teardown_disposition TEXT NOT NULL DEFAULT '',
	teardown_herdr_state TEXT NOT NULL DEFAULT '',
	teardown_worktree_state TEXT NOT NULL DEFAULT '',
	teardown_completion_state TEXT NOT NULL DEFAULT '',
	usage_limit_episode INTEGER NOT NULL DEFAULT 0,
	usage_limit_stuck_episode INTEGER NOT NULL DEFAULT 0,
	UNIQUE (task_id, ordinal)
);
CREATE UNIQUE INDEX IF NOT EXISTS attempt_one_active
ON attempt(task_id)
WHERE lifecycle IN ('provisioning', 'running');
CREATE TABLE IF NOT EXISTS project (
	name     TEXT PRIMARY KEY,
	url      TEXT NOT NULL,
	mode     TEXT NOT NULL,
	position INTEGER NOT NULL,
	upstream TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS fleet_identity (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	fleet_id TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS hold (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL DEFAULT '',
	reason     TEXT NOT NULL DEFAULT '',
	blocked_on TEXT NOT NULL DEFAULT '',
	set_at     TEXT NOT NULL DEFAULT '',
	inferred   INTEGER NOT NULL DEFAULT 0
);
`

const sendSchema = `
CREATE TABLE IF NOT EXISTS send_attempt (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id      TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
	attempt_id   INTEGER NOT NULL REFERENCES attempt(id) ON DELETE CASCADE,
	origin       TEXT NOT NULL CHECK (origin IN ('operator', 'usage-limit-resume', 'legacy-undelivered')),
	message      TEXT NOT NULL,
	state        TEXT NOT NULL CHECK (state IN ('pending', 'not-submitted', 'submitted', 'uncertain')),
	reason_code  TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	finalized_at TEXT NOT NULL DEFAULT '',
	usage_limit_episode INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS send_attempt_one_pending
ON send_attempt(attempt_id)
WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS send_attempt_latest
ON send_attempt(task_id, attempt_id, origin, id DESC);
CREATE INDEX IF NOT EXISTS send_attempt_latest_any
ON send_attempt(task_id, attempt_id, id DESC);
CREATE INDEX IF NOT EXISTS send_attempt_pending_lookup
ON send_attempt(task_id, attempt_id, state);
`

// Shared by the machine-state database and the derived index. The busy timeout
// matters because several hand commands can run at once, and sqlite's default
// is to fail the second write immediately rather than wait.
func open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	// The pragmas need a file: URI, and a URI means sqlite reads `%`, `#` and
	// `?` in the fleet home's path as syntax rather than as filename.
	uri := "file:" + (&url.URL{Path: path}).EscapedPath() + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One connection, because every writer here is a short-lived CLI process and
	// a pool would let sqlite fail a second connection's write against the first.
	db.SetMaxOpenConns(1)
	return db, nil
}

// Safe to call on every command: creating the database and importing any
// pre-sqlite state are both idempotent.
func Open(homeDir string) (*DB, error) {
	sqlDB, err := open(Path(homeDir))
	if err != nil {
		return nil, err
	}
	db := &DB{sql: sqlDB, home: homeDir}
	if err := db.migrateSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := db.migrateLegacy(true); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if len(migrations) >= fleetIdentityVersion {
		if _, err := db.FleetID(); err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
	}
	return db, nil
}

func openReadOnly(homeDir string) (*DB, error) {
	path := Path(homeDir)
	uri := "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=query_only(1)"
	sqlDB, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	return &DB{sql: sqlDB, home: homeDir}, nil
}

// Presentation readers use an existing-file handle so SELECTs cannot create schema or
// import legacy state. An older layout has to cross an explicit migration boundary first.
func OpenReadOnly(homeDir string) (*DB, error) {
	if _, err := os.Stat(Path(homeDir)); os.IsNotExist(err) {
		sqlDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, fmt.Errorf("open read-only state: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
		db := &DB{sql: sqlDB, home: homeDir, empty: true}
		if err := db.createSchema(true, len(migrations)); err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
		if err := db.migrateLegacy(false); err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
		db.empty = false
		if _, err := sqlDB.Exec("PRAGMA query_only = 1"); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("set read-only state: %w", err)
		}
		return db, nil
	} else if err != nil {
		return nil, fmt.Errorf("check %s: %w", Path(homeDir), err)
	}
	db, err := openReadOnly(homeDir)
	if err != nil {
		return nil, err
	}
	current, err := db.schemaVersion()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	latest := len(migrations)
	empty, err := db.isNewDatabase()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if empty {
		entries, err := os.ReadDir(Dir(homeDir))
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".json" {
				_ = db.Close()
				return nil, fmt.Errorf("state/hand.db is schema version %d, older than this build of hand requires (version %d) - run `hand init %s` before opening it read-only", current, latest, shellquote.Quote(homeDir))
			}
		}
		db.empty = true
		return db, nil
	}
	if err := schemaVersionError(current, latest); err != nil {
		_ = db.Close()
		return nil, err
	}
	if current < latest {
		_ = db.Close()
		return nil, fmt.Errorf("state/hand.db is schema version %d, older than this build of hand requires (version %d) - run `hand init %s` before opening it read-only", current, latest, shellquote.Quote(homeDir))
	}
	return db, nil
}

// ValidateInitTarget checks that an existing state database can be opened by the
// migration path without running that migration.
func ValidateInitTarget(homeDir string) error {
	if _, err := os.Stat(Path(homeDir)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check %s: %w", Path(homeDir), err)
	}
	db, err := openReadOnly(homeDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	current, err := db.schemaVersion()
	if err != nil {
		return err
	}
	return schemaVersionError(current, len(migrations))
}

func openReadOnlyForLifecycle(homeDir string) (*DB, int, error) {
	if _, err := os.Stat(Path(homeDir)); os.IsNotExist(err) {
		db, err := OpenReadOnly(homeDir)
		if err != nil {
			return nil, 0, err
		}
		return db, len(migrations), nil
	} else if err != nil {
		return nil, 0, fmt.Errorf("check %s: %w", Path(homeDir), err)
	}
	db, err := openReadOnly(homeDir)
	if err != nil {
		return nil, 0, err
	}
	current, err := db.schemaVersion()
	if err != nil {
		_ = db.Close()
		return nil, 0, err
	}
	latest := len(migrations)
	if err := schemaVersionError(current, latest); err != nil {
		_ = db.Close()
		return nil, 0, err
	}
	empty, err := db.isNewDatabase()
	if err != nil {
		_ = db.Close()
		return nil, 0, err
	}
	if empty {
		db.empty = true
		return db, current, nil
	}
	if current < teardownEvidenceVersion {
		_ = db.Close()
		return nil, 0, fmt.Errorf("state/hand.db is schema version %d, older than this build of hand requires (version %d) - run `hand init %s` before opening it read-only", current, latest, shellquote.Quote(homeDir))
	}
	return db, current, nil
}

// Lifecycle preflight accepts the immediately previous schema because it needs task identity
// before routing validation, while migration must wait until after that validation.
func ReadTaskHistoryReadOnly(homeDir, id string) (TaskHistory, bool, error) {
	db, current, err := openReadOnlyForLifecycle(homeDir)
	if err != nil {
		return TaskHistory{}, false, err
	}
	defer func() { _ = db.Close() }()
	if current == len(migrations) {
		return db.ReadTaskHistory(id)
	}
	if current == acknowledgedMetadataVersion {
		return db.ReadTaskHistory(id)
	}
	if current == preAcknowledgementVersion || current == attemptBranchVersion {
		return db.readTaskHistoryBeforeAcknowledgement(id)
	}
	if current == holdInferredVersion {
		return db.readTaskHistoryBeforeAcknowledgementAndBranch(id)
	}
	if current == sendSchemaVersion {
		return db.readTaskHistoryBeforeSend(id)
	}
	if current == repairMetadataVersion {
		return db.readTaskHistoryBeforeSend(id)
	}
	if current == repairMetadataVersion-1 {
		return db.readTaskHistoryBeforeRepair(id)
	}
	return db.readTaskHistoryBeforeRouting(id)
}

// Lifecycle preflight can inspect holds from the current or immediately previous schema
// without importing legacy state or migrating the database.
func ReadHoldReadOnly(homeDir, id string) (Hold, bool, error) {
	db, current, err := openReadOnlyForLifecycle(homeDir)
	if err != nil {
		return Hold{}, false, err
	}
	defer func() { _ = db.Close() }()
	if current <= holdInferredVersion {
		return db.readHoldBeforeInferred(id)
	}
	return db.ReadHold(id)
}

func (db *DB) Close() error {
	return db.sql.Close()
}

func (db *DB) meta(key string) (string, error) {
	var value string
	err := db.sql.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read meta %q: %w", key, err)
	}
	return value, nil
}

func (db *DB) setMeta(key, value string) error {
	_, err := db.sql.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write meta %q: %w", key, err)
	}
	return nil
}

var taskColumnNames = []string{
	"id", "project", "kind", "brief", "lifecycle", "active_attempt_id", "pr",
	"merge_executed", "merge_executed_at", "merge_announced", "delivered_at", "delivered_reason",
	"report_offset", "report_digest", "created_at", "repair_code", "repair_reason", "repair_attempt_id", "repair_observed_at",
	"acknowledged_at", "acknowledged_reason", "acknowledged_offset", "acknowledged_digest",
}

var taskColumns = strings.Join(taskColumnNames, ", ")

var taskColumnsBeforeRepair = strings.Join(taskColumnNames[:len(taskColumnNames)-8], ", ")

var taskColumnsBeforeAcknowledgement = strings.Join(taskColumnNames[:len(taskColumnNames)-4], ", ")

var attemptColumnNames = []string{
	"id", "task_id", "ordinal", "lifecycle", "harness", "model", "effort",
	"execution_class", "planned_against", "requested_profile", "routing_source", "worktree", "lease_id",
	"herdr_session", "herdr_workspace_id", "herdr_tab_id", "herdr_pane_id", "created_at", "pane_started_at",
	"launch_submitted_at", "launch_confirmed_at",
	"status_changed_at", "status_changed_for", "done_verified", "last_report_state", "last_report_note",
	"send_undelivered_message", "send_undelivered_at", "parked_fired_for", "usage_limit_retry_at", "usage_limit_attempts",
	"teardown_terminal_attempt", "teardown_disposition", "teardown_herdr_state", "teardown_worktree_state", "teardown_completion_state",
	"usage_limit_episode", "usage_limit_stuck_episode", "branch",
}

var attemptColumns = strings.Join(attemptColumnNames, ", ")

// -3 excludes usage_limit_episode, usage_limit_stuck_episode and branch: all three were added
// after the send schema, in that order, at the tail of attemptColumnNames.
var attemptColumnsBeforeSend = strings.Join(attemptColumnNames[:len(attemptColumnNames)-3], ", ")

var attemptColumnNamesBeforeRouting = append(append([]string{}, attemptColumnNames[:7]...), attemptColumnNames[11:len(attemptColumnNames)-3]...)
var attemptColumnsBeforeRouting = strings.Join(attemptColumnNamesBeforeRouting, ", ")

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

func taskValues(t Task) []any {
	return []any{t.ID, t.Project, t.Kind, t.Brief, t.Lifecycle, nullableAttemptID(t.ActiveAttemptID), t.PR,
		t.MergeExecuted, t.MergeExecutedAt, t.MergeAnnounced, t.DeliveredAt, t.DeliveredReason,
		t.ReportOffset, t.ReportDigest, t.CreatedAt, t.RepairCode, t.RepairReason, t.RepairAttemptID, t.RepairObservedAt,
		t.AcknowledgedAt, t.AcknowledgedReason, t.AcknowledgedOffset, t.AcknowledgedDigest}
}

func nullableAttemptID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var activeID sql.NullInt64
	err := row.Scan(&t.ID, &t.Project, &t.Kind, &t.Brief, &t.Lifecycle, &activeID, &t.PR,
		&t.MergeExecuted, &t.MergeExecutedAt, &t.MergeAnnounced, &t.DeliveredAt, &t.DeliveredReason,
		&t.ReportOffset, &t.ReportDigest, &t.CreatedAt, &t.RepairCode, &t.RepairReason, &t.RepairAttemptID, &t.RepairObservedAt,
		&t.AcknowledgedAt, &t.AcknowledgedReason, &t.AcknowledgedOffset, &t.AcknowledgedDigest)
	if activeID.Valid {
		t.ActiveAttemptID = activeID.Int64
	}
	if t.Lifecycle == "" {
		t.Lifecycle = TaskOpen
	}
	return t, err
}

// Reads a task row from a database migrated no further than sendSchemaVersion, one version behind
// the acknowledgement columns atqamz/hand#267 added - the read-only ladder's guard against selecting
// columns that migrateSchema has not yet added to this home.
func scanTaskBeforeAcknowledgement(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var activeID sql.NullInt64
	err := row.Scan(&t.ID, &t.Project, &t.Kind, &t.Brief, &t.Lifecycle, &activeID, &t.PR,
		&t.MergeExecuted, &t.MergeExecutedAt, &t.MergeAnnounced, &t.DeliveredAt, &t.DeliveredReason,
		&t.ReportOffset, &t.ReportDigest, &t.CreatedAt, &t.RepairCode, &t.RepairReason, &t.RepairAttemptID, &t.RepairObservedAt)
	if activeID.Valid {
		t.ActiveAttemptID = activeID.Int64
	}
	if t.Lifecycle == "" {
		t.Lifecycle = TaskOpen
	}
	return t, err
}

func scanTaskBeforeRepair(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var activeID sql.NullInt64
	err := row.Scan(&t.ID, &t.Project, &t.Kind, &t.Brief, &t.Lifecycle, &activeID, &t.PR,
		&t.MergeExecuted, &t.MergeExecutedAt, &t.MergeAnnounced, &t.DeliveredAt, &t.DeliveredReason,
		&t.ReportOffset, &t.ReportDigest, &t.CreatedAt)
	if activeID.Valid {
		t.ActiveAttemptID = activeID.Int64
	}
	if t.Lifecycle == "" {
		t.Lifecycle = TaskOpen
	}
	return t, err
}

func attemptValues(a Attempt) []any {
	return []any{a.ID, a.TaskID, a.Ordinal, a.Lifecycle, a.Harness, a.Model, a.Effort,
		a.ExecutionClass, a.PlannedAgainst, a.RequestedProfile, a.RoutingSource, a.Worktree, a.LeaseID,
		a.Herdr.Session, a.Herdr.WorkspaceID, a.Herdr.TabID, a.Herdr.PaneID, a.CreatedAt, a.PaneStartedAt,
		a.LaunchSubmittedAt, a.LaunchConfirmedAt,
		a.StatusChangedAt, a.StatusChangedFor, a.DoneVerified, a.LastReportState, a.LastReportNote,
		a.SendUndeliveredMessage, a.SendUndeliveredAt, a.ParkedFiredFor, a.UsageLimitRetryAt, a.UsageLimitAttempts,
		a.TeardownTerminalAttempt, a.TeardownDisposition, a.TeardownHerdrState, a.TeardownWorktreeState, a.TeardownCompletionState,
		a.UsageLimitEpisode, a.UsageLimitStuckEpisode, a.Branch}
}

func scanAttempt(row interface{ Scan(...any) error }) (Attempt, error) {
	var a Attempt
	err := row.Scan(&a.ID, &a.TaskID, &a.Ordinal, &a.Lifecycle, &a.Harness, &a.Model, &a.Effort,
		&a.ExecutionClass, &a.PlannedAgainst, &a.RequestedProfile, &a.RoutingSource, &a.Worktree, &a.LeaseID,
		&a.Herdr.Session, &a.Herdr.WorkspaceID, &a.Herdr.TabID, &a.Herdr.PaneID, &a.CreatedAt, &a.PaneStartedAt,
		&a.LaunchSubmittedAt, &a.LaunchConfirmedAt,
		&a.StatusChangedAt, &a.StatusChangedFor, &a.DoneVerified, &a.LastReportState, &a.LastReportNote,
		&a.SendUndeliveredMessage, &a.SendUndeliveredAt, &a.ParkedFiredFor, &a.UsageLimitRetryAt, &a.UsageLimitAttempts,
		&a.TeardownTerminalAttempt, &a.TeardownDisposition, &a.TeardownHerdrState, &a.TeardownWorktreeState, &a.TeardownCompletionState,
		&a.UsageLimitEpisode, &a.UsageLimitStuckEpisode, &a.Branch)
	return a, err
}

func scanAttemptBeforeRouting(row interface{ Scan(...any) error }) (Attempt, error) {
	var a Attempt
	err := row.Scan(&a.ID, &a.TaskID, &a.Ordinal, &a.Lifecycle, &a.Harness, &a.Model, &a.Effort,
		&a.Worktree, &a.LeaseID, &a.Herdr.Session, &a.Herdr.WorkspaceID, &a.Herdr.TabID, &a.Herdr.PaneID,
		&a.CreatedAt, &a.PaneStartedAt, &a.LaunchSubmittedAt, &a.LaunchConfirmedAt,
		&a.StatusChangedAt, &a.StatusChangedFor, &a.DoneVerified, &a.LastReportState, &a.LastReportNote,
		&a.SendUndeliveredMessage, &a.SendUndeliveredAt, &a.ParkedFiredFor, &a.UsageLimitRetryAt, &a.UsageLimitAttempts,
		&a.TeardownTerminalAttempt, &a.TeardownDisposition, &a.TeardownHerdrState, &a.TeardownWorktreeState, &a.TeardownCompletionState)
	return a, err
}

func scanAttemptBeforeSend(row interface{ Scan(...any) error }) (Attempt, error) {
	var a Attempt
	err := row.Scan(&a.ID, &a.TaskID, &a.Ordinal, &a.Lifecycle, &a.Harness, &a.Model, &a.Effort,
		&a.ExecutionClass, &a.PlannedAgainst, &a.RequestedProfile, &a.RoutingSource, &a.Worktree, &a.LeaseID,
		&a.Herdr.Session, &a.Herdr.WorkspaceID, &a.Herdr.TabID, &a.Herdr.PaneID, &a.CreatedAt, &a.PaneStartedAt,
		&a.LaunchSubmittedAt, &a.LaunchConfirmedAt,
		&a.StatusChangedAt, &a.StatusChangedFor, &a.DoneVerified, &a.LastReportState, &a.LastReportNote,
		&a.SendUndeliveredMessage, &a.SendUndeliveredAt, &a.ParkedFiredFor, &a.UsageLimitRetryAt, &a.UsageLimitAttempts,
		&a.TeardownTerminalAttempt, &a.TeardownDisposition, &a.TeardownHerdrState, &a.TeardownWorktreeState, &a.TeardownCompletionState)
	return a, err
}

func (db *DB) ReadTask(id string) (Task, bool, error) {
	row := db.sql.QueryRow(`SELECT `+taskColumns+` FROM task WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("read task %q: %w", id, err)
	}
	return t, true, nil
}

func (db *DB) CreateTask(t Task) error {
	if t.Lifecycle == "" {
		t.Lifecycle = TaskOpen
	}
	_, err := db.sql.Exec(`INSERT INTO task (`+taskColumns+`) VALUES (`+placeholders(len(taskColumnNames))+`)`, taskValues(t)...)
	if err != nil {
		return fmt.Errorf("create task %q: %w", t.ID, err)
	}
	return nil
}

func (db *DB) UpdateTask(t Task) error {
	_, err := db.sql.Exec(`UPDATE task SET project = ?, kind = ?, brief = ?,
		pr = ?, merge_executed = ?, merge_executed_at = ?, merge_announced = ?, delivered_at = ?, delivered_reason = ?,
		report_offset = ?, report_digest = ?, created_at = ?, repair_code = ?, repair_reason = ?, repair_attempt_id = ?, repair_observed_at = ?,
		acknowledged_at = ?, acknowledged_reason = ?, acknowledged_offset = ?, acknowledged_digest = ? WHERE id = ?`,
		t.Project, t.Kind, t.Brief, t.PR,
		t.MergeExecuted, t.MergeExecutedAt, t.MergeAnnounced, t.DeliveredAt, t.DeliveredReason,
		t.ReportOffset, t.ReportDigest, t.CreatedAt, t.RepairCode, t.RepairReason, t.RepairAttemptID, t.RepairObservedAt,
		t.AcknowledgedAt, t.AcknowledgedReason, t.AcknowledgedOffset, t.AcknowledgedDigest, t.ID)
	if err != nil {
		return fmt.Errorf("update task %q: %w", t.ID, err)
	}
	return nil
}

func (db *DB) ListTasks() ([]Task, error) {
	rows, err := db.sql.Query(`SELECT ` + taskColumns + ` FROM task ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, nil
}

func (db *DB) ReadTaskHistory(id string) (TaskHistory, bool, error) {
	if db.empty {
		return TaskHistory{}, false, nil
	}
	task, found, err := db.ReadTask(id)
	if err != nil || !found {
		return TaskHistory{}, found, err
	}
	attempts, err := db.ListAttempts(id)
	if err != nil {
		return TaskHistory{}, false, err
	}
	history := TaskHistory{Task: task, Attempts: attempts}
	for i := range attempts {
		if attempts[i].ID == task.ActiveAttemptID {
			history.ActiveAttempt = &attempts[i]
			break
		}
	}
	if task.ActiveAttemptID != 0 && history.ActiveAttempt == nil {
		return TaskHistory{}, false, fmt.Errorf("read task history %q: active attempt %d not found", id, task.ActiveAttemptID)
	}
	return history, true, nil
}

func (db *DB) readTaskHistoryBeforeRouting(id string) (TaskHistory, bool, error) {
	if db.empty {
		return TaskHistory{}, false, nil
	}
	row := db.sql.QueryRow(`SELECT `+taskColumnsBeforeRepair+` FROM task WHERE id = ?`, id)
	task, err := scanTaskBeforeRepair(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskHistory{}, false, nil
	}
	if err != nil {
		return TaskHistory{}, false, fmt.Errorf("read task %q: %w", id, err)
	}
	return db.readTaskHistoryBeforeRoutingWithTask(task)
}

func (db *DB) readTaskHistoryBeforeRepair(id string) (TaskHistory, bool, error) {
	if db.empty {
		return TaskHistory{}, false, nil
	}
	row := db.sql.QueryRow(`SELECT `+taskColumnsBeforeRepair+` FROM task WHERE id = ?`, id)
	task, err := scanTaskBeforeRepair(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskHistory{}, false, nil
	}
	rows, err := db.sql.Query(`SELECT `+attemptColumnsBeforeSend+` FROM attempt WHERE task_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return TaskHistory{}, false, fmt.Errorf("list attempts for task %q: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	var attempts []Attempt
	for rows.Next() {
		attempt, err := scanAttemptBeforeSend(rows)
		if err != nil {
			return TaskHistory{}, false, fmt.Errorf("list attempts for task %q: %w", id, err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return TaskHistory{}, false, fmt.Errorf("list attempts for task %q: %w", id, err)
	}
	history := TaskHistory{Task: task, Attempts: attempts}
	for i := range attempts {
		if attempts[i].ID == task.ActiveAttemptID {
			history.ActiveAttempt = &attempts[i]
			break
		}
	}
	if task.ActiveAttemptID != 0 && history.ActiveAttempt == nil {
		return TaskHistory{}, false, fmt.Errorf("read task history %q: active attempt %d not found", id, task.ActiveAttemptID)
	}
	return history, true, nil
}

func (db *DB) readTaskHistoryBeforeAcknowledgement(id string) (TaskHistory, bool, error) {
	if db.empty {
		return TaskHistory{}, false, nil
	}
	row := db.sql.QueryRow(`SELECT `+taskColumnsBeforeAcknowledgement+` FROM task WHERE id = ?`, id)
	task, err := scanTaskBeforeAcknowledgement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskHistory{}, false, nil
	}
	if err != nil {
		return TaskHistory{}, false, fmt.Errorf("read task %q: %w", id, err)
	}
	attempts, err := db.ListAttempts(id)
	if err != nil {
		return TaskHistory{}, false, err
	}
	history := TaskHistory{Task: task, Attempts: attempts}
	for i := range attempts {
		if attempts[i].ID == task.ActiveAttemptID {
			history.ActiveAttempt = &attempts[i]
			break
		}
	}
	if task.ActiveAttemptID != 0 && history.ActiveAttempt == nil {
		return TaskHistory{}, false, fmt.Errorf("read task history %q: active attempt %d not found", id, task.ActiveAttemptID)
	}
	return history, true, nil
}

// A database at holdInferredVersion has every column readTaskHistoryBeforeAcknowledgement's task side
// needs, but not yet attempt.branch (added going into attemptBranchVersion), so its attempt side has to
// stay on the pre-branch reader rather than db.ListAttempts, which now selects branch unconditionally.
func (db *DB) readTaskHistoryBeforeAcknowledgementAndBranch(id string) (TaskHistory, bool, error) {
	if db.empty {
		return TaskHistory{}, false, nil
	}
	row := db.sql.QueryRow(`SELECT `+taskColumnsBeforeAcknowledgement+` FROM task WHERE id = ?`, id)
	task, err := scanTaskBeforeAcknowledgement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskHistory{}, false, nil
	}
	if err != nil {
		return TaskHistory{}, false, fmt.Errorf("read task %q: %w", id, err)
	}
	attempts, err := db.listAttemptsBeforeSend(id)
	if err != nil {
		return TaskHistory{}, false, err
	}
	history := TaskHistory{Task: task, Attempts: attempts}
	for i := range attempts {
		if attempts[i].ID == task.ActiveAttemptID {
			history.ActiveAttempt = &attempts[i]
			break
		}
	}
	if task.ActiveAttemptID != 0 && history.ActiveAttempt == nil {
		return TaskHistory{}, false, fmt.Errorf("read task history %q: active attempt %d not found", id, task.ActiveAttemptID)
	}
	return history, true, nil
}

func (db *DB) readTaskHistoryBeforeSend(id string) (TaskHistory, bool, error) {
	if db.empty {
		return TaskHistory{}, false, nil
	}
	row := db.sql.QueryRow(`SELECT `+taskColumnsBeforeAcknowledgement+` FROM task WHERE id = ?`, id)
	task, err := scanTaskBeforeAcknowledgement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskHistory{}, false, nil
	}
	if err != nil {
		return TaskHistory{}, false, fmt.Errorf("read task %q: %w", id, err)
	}
	attempts, err := db.listAttemptsBeforeSend(id)
	if err != nil {
		return TaskHistory{}, false, err
	}
	history := TaskHistory{Task: task, Attempts: attempts}
	for i := range attempts {
		if attempts[i].ID == task.ActiveAttemptID {
			history.ActiveAttempt = &attempts[i]
			break
		}
	}
	if task.ActiveAttemptID != 0 && history.ActiveAttempt == nil {
		return TaskHistory{}, false, fmt.Errorf("read task history %q: active attempt %d not found", id, task.ActiveAttemptID)
	}
	return history, true, nil
}

func (db *DB) readTaskHistoryBeforeRoutingWithTask(task Task) (TaskHistory, bool, error) {
	rows, err := db.sql.Query(`SELECT `+attemptColumnsBeforeRouting+` FROM attempt WHERE task_id = ? ORDER BY ordinal`, task.ID)
	if err != nil {
		return TaskHistory{}, false, fmt.Errorf("list attempts for task %q: %w", task.ID, err)
	}
	defer func() { _ = rows.Close() }()
	var attempts []Attempt
	for rows.Next() {
		attempt, err := scanAttemptBeforeRouting(rows)
		if err != nil {
			return TaskHistory{}, false, fmt.Errorf("list attempts for task %q: %w", task.ID, err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return TaskHistory{}, false, fmt.Errorf("list attempts for task %q: %w", task.ID, err)
	}
	history := TaskHistory{Task: task, Attempts: attempts}
	for i := range attempts {
		if attempts[i].ID == task.ActiveAttemptID {
			history.ActiveAttempt = &attempts[i]
			break
		}
	}
	if task.ActiveAttemptID != 0 && history.ActiveAttempt == nil {
		return TaskHistory{}, false, fmt.Errorf("read task history %q: active attempt %d not found", task.ID, task.ActiveAttemptID)
	}
	return history, true, nil
}

func (db *DB) listAttemptsBeforeSend(taskID string) ([]Attempt, error) {
	rows, err := db.sql.Query(`SELECT `+attemptColumnsBeforeSend+` FROM attempt WHERE task_id = ? ORDER BY ordinal`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list attempts for task %q: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var attempts []Attempt
	for rows.Next() {
		attempt, err := scanAttemptBeforeSend(rows)
		if err != nil {
			return nil, fmt.Errorf("list attempts for task %q: %w", taskID, err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list attempts for task %q: %w", taskID, err)
	}
	return attempts, nil
}

func (db *DB) ListOpenTaskHistories() ([]TaskHistory, error) {
	if db.empty {
		return nil, nil
	}
	rows, err := db.sql.Query(`SELECT ` + taskColumns + ` FROM task WHERE lifecycle = 'open' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list open tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var histories []TaskHistory
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("list open tasks: %w", err)
		}
		histories = append(histories, TaskHistory{Task: task})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open tasks: %w", err)
	}
	for i := range histories {
		attempts, err := db.ListAttempts(histories[i].Task.ID)
		if err != nil {
			return nil, err
		}
		histories[i].Attempts = attempts
		for j := range attempts {
			if attempts[j].ID == histories[i].Task.ActiveAttemptID {
				histories[i].ActiveAttempt = &histories[i].Attempts[j]
				break
			}
		}
		if histories[i].Task.ActiveAttemptID != 0 && histories[i].ActiveAttempt == nil {
			return nil, fmt.Errorf("task %q active attempt %d not found", histories[i].Task.ID, histories[i].Task.ActiveAttemptID)
		}
	}
	return histories, nil
}

func (db *DB) ListReconciliationHistories() ([]TaskHistory, error) {
	if db.empty {
		return nil, nil
	}
	rows, err := db.sql.Query(`SELECT ` + taskColumns + ` FROM task WHERE lifecycle = 'open' OR repair_code <> '' OR EXISTS (
		SELECT 1 FROM attempt
		WHERE attempt.task_id = task.id
		AND attempt.lifecycle NOT IN ('provisioning', 'running')
		AND (
			(attempt.worktree <> '' AND attempt.teardown_worktree_state NOT IN ('released', 'abandoned'))
			OR (attempt.herdr_workspace_id <> '' AND attempt.teardown_herdr_state <> 'released')
			OR (attempt.teardown_completion_state <> '' AND attempt.teardown_completion_state <> 'appended')
		)
	) ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list reconciliation tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var histories []TaskHistory
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("list reconciliation tasks: %w", err)
		}
		histories = append(histories, TaskHistory{Task: task})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reconciliation tasks: %w", err)
	}
	for i := range histories {
		attempts, err := db.ListAttempts(histories[i].Task.ID)
		if err != nil {
			return nil, err
		}
		histories[i].Attempts = attempts
		for j := range attempts {
			if attempts[j].ID == histories[i].Task.ActiveAttemptID {
				histories[i].ActiveAttempt = &histories[i].Attempts[j]
				break
			}
		}
		if histories[i].Task.ActiveAttemptID != 0 && histories[i].ActiveAttempt == nil {
			return nil, fmt.Errorf("task %q active attempt %d not found", histories[i].Task.ID, histories[i].Task.ActiveAttemptID)
		}
	}
	return histories, nil
}

func ListReconciliationHistoriesReadOnly(homeDir string) ([]TaskHistory, error) {
	db, current, err := openReadOnlyForLifecycle(homeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	if current == preAcknowledgementVersion || current == attemptBranchVersion {
		return db.listReconciliationHistoriesBeforeAcknowledgement()
	}
	if current == holdInferredVersion {
		return db.listReconciliationHistoriesBeforeAcknowledgementAndBranch()
	}
	if current == acknowledgedMetadataVersion {
		return db.ListReconciliationHistories()
	}
	if current == repairMetadataVersion || current == sendSchemaVersion {
		return db.listReconciliationHistoriesBeforeSend()
	}
	return db.ListReconciliationHistories()
}

// The reconciliation-listing counterpart to readTaskHistoryBeforeAcknowledgementAndBranch: holdInferredVersion
// predates attempt.branch, so its attempt side stays on the pre-branch reader.
func (db *DB) listReconciliationHistoriesBeforeAcknowledgementAndBranch() ([]TaskHistory, error) {
	if db.empty {
		return nil, nil
	}
	rows, err := db.sql.Query(`SELECT ` + taskColumnsBeforeAcknowledgement + ` FROM task WHERE lifecycle = 'open' OR repair_code <> '' OR EXISTS (
		SELECT 1 FROM attempt
		WHERE attempt.task_id = task.id
		AND attempt.lifecycle NOT IN ('provisioning', 'running')
		AND (
			(attempt.worktree <> '' AND attempt.teardown_worktree_state NOT IN ('released', 'abandoned'))
			OR (attempt.herdr_workspace_id <> '' AND attempt.teardown_herdr_state <> 'released')
			OR (attempt.teardown_completion_state <> '' AND attempt.teardown_completion_state <> 'appended')
		)
	) ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var histories []TaskHistory
	for rows.Next() {
		task, err := scanTaskBeforeAcknowledgement(rows)
		if err != nil {
			return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
		}
		histories = append(histories, TaskHistory{Task: task})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
	}
	for i := range histories {
		attempts, err := db.listAttemptsBeforeSend(histories[i].Task.ID)
		if err != nil {
			return nil, err
		}
		histories[i].Attempts = attempts
		for j := range attempts {
			if attempts[j].ID == histories[i].Task.ActiveAttemptID {
				histories[i].ActiveAttempt = &histories[i].Attempts[j]
				break
			}
		}
		if histories[i].Task.ActiveAttemptID != 0 && histories[i].ActiveAttempt == nil {
			return nil, fmt.Errorf("read task history %q: active attempt %d not found", histories[i].Task.ID, histories[i].Task.ActiveAttemptID)
		}
	}
	return histories, nil
}

func (db *DB) listReconciliationHistoriesBeforeAcknowledgement() ([]TaskHistory, error) {
	if db.empty {
		return nil, nil
	}
	rows, err := db.sql.Query(`SELECT ` + taskColumnsBeforeAcknowledgement + ` FROM task WHERE lifecycle = 'open' OR repair_code <> '' OR EXISTS (
		SELECT 1 FROM attempt
		WHERE attempt.task_id = task.id
		AND attempt.lifecycle NOT IN ('provisioning', 'running')
		AND (
			(attempt.worktree <> '' AND attempt.teardown_worktree_state NOT IN ('released', 'abandoned'))
			OR (attempt.herdr_workspace_id <> '' AND attempt.teardown_herdr_state <> 'released')
			OR (attempt.teardown_completion_state <> '' AND attempt.teardown_completion_state <> 'appended')
		)
	) ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var histories []TaskHistory
	for rows.Next() {
		task, err := scanTaskBeforeAcknowledgement(rows)
		if err != nil {
			return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
		}
		histories = append(histories, TaskHistory{Task: task})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
	}
	for i := range histories {
		attempts, err := db.ListAttempts(histories[i].Task.ID)
		if err != nil {
			return nil, err
		}
		histories[i].Attempts = attempts
		for j := range attempts {
			if attempts[j].ID == histories[i].Task.ActiveAttemptID {
				histories[i].ActiveAttempt = &histories[i].Attempts[j]
				break
			}
		}
		if histories[i].Task.ActiveAttemptID != 0 && histories[i].ActiveAttempt == nil {
			return nil, fmt.Errorf("read task history %q: active attempt %d not found", histories[i].Task.ID, histories[i].Task.ActiveAttemptID)
		}
	}
	return histories, nil
}

func (db *DB) listReconciliationHistoriesBeforeSend() ([]TaskHistory, error) {
	if db.empty {
		return nil, nil
	}
	rows, err := db.sql.Query(`SELECT ` + taskColumnsBeforeAcknowledgement + ` FROM task WHERE lifecycle = 'open' OR repair_code <> '' OR EXISTS (
		SELECT 1 FROM attempt
		WHERE attempt.task_id = task.id
		AND attempt.lifecycle NOT IN ('provisioning', 'running')
		AND (
			(attempt.worktree <> '' AND attempt.teardown_worktree_state NOT IN ('released', 'abandoned'))
			OR (attempt.herdr_workspace_id <> '' AND attempt.teardown_herdr_state <> 'released')
			OR (attempt.teardown_completion_state <> '' AND attempt.teardown_completion_state <> 'appended')
		)
	) ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var histories []TaskHistory
	for rows.Next() {
		task, err := scanTaskBeforeAcknowledgement(rows)
		if err != nil {
			return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
		}
		histories = append(histories, TaskHistory{Task: task})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list read-only reconciliation tasks: %w", err)
	}
	for i := range histories {
		attempts, err := db.listAttemptsBeforeSend(histories[i].Task.ID)
		if err != nil {
			return nil, err
		}
		histories[i].Attempts = attempts
		for j := range attempts {
			if attempts[j].ID == histories[i].Task.ActiveAttemptID {
				histories[i].ActiveAttempt = &histories[i].Attempts[j]
				break
			}
		}
		if histories[i].Task.ActiveAttemptID != 0 && histories[i].ActiveAttempt == nil {
			return nil, fmt.Errorf("read task history %q: active attempt %d not found", histories[i].Task.ID, histories[i].Task.ActiveAttemptID)
		}
	}
	return histories, nil
}

func (db *DB) ListHerdrOwnerships() ([]HerdrOwnership, error) {
	rows, err := db.sql.Query(`SELECT id, task_id, lifecycle, teardown_herdr_state, herdr_session, herdr_workspace_id, herdr_tab_id, herdr_pane_id FROM attempt WHERE herdr_workspace_id <> '' OR herdr_tab_id <> '' OR herdr_pane_id <> '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list Herdr ownerships: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ownerships []HerdrOwnership
	for rows.Next() {
		var ownership HerdrOwnership
		if err := rows.Scan(&ownership.AttemptID, &ownership.TaskID, &ownership.Lifecycle, &ownership.TeardownHerdrState, &ownership.Session, &ownership.WorkspaceID, &ownership.TabID, &ownership.PaneID); err != nil {
			return nil, fmt.Errorf("list Herdr ownerships: %w", err)
		}
		ownerships = append(ownerships, ownership)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Herdr ownerships: %w", err)
	}
	return ownerships, nil
}

func (db *DB) CreateTaskWithAttempt(t Task, a Attempt) (Attempt, error) {
	if t.Lifecycle == "" {
		t.Lifecycle = TaskOpen
	}
	if a.TaskID == "" {
		a.TaskID = t.ID
	}
	if a.TaskID != t.ID || a.Lifecycle != AttemptProvisioning {
		return Attempt{}, fmt.Errorf("%w: task creation requires a matching provisioning attempt", ErrInvalidTransition)
	}
	t.ActiveAttemptID = 0
	a.Ordinal = 0
	tx, err := db.beginLifecycleTx("create task", t.ID)
	if err != nil {
		return Attempt{}, fmt.Errorf("begin task creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO task (`+taskColumns+`) VALUES (`+placeholders(len(taskColumnNames))+`)`, taskValues(t)...); err != nil {
		if isSQLiteBusy(err) {
			return Attempt{}, contention("create task", t.ID, err)
		}
		if isSQLiteConstraint(err) {
			return Attempt{}, fmt.Errorf("%w: task %q already exists", ErrLifecycleConflict, t.ID)
		}
		return Attempt{}, fmt.Errorf("create task %q: %w", t.ID, err)
	}
	created, err := insertAttemptTx(tx, a)
	if err != nil {
		return Attempt{}, fmt.Errorf("create initial attempt for task %q: %w", t.ID, err)
	}
	result, err := tx.Exec(`UPDATE task SET active_attempt_id = ? WHERE id = ? AND lifecycle = 'open' AND active_attempt_id IS NULL`, created.ID, t.ID)
	if err != nil {
		return Attempt{}, fmt.Errorf("set initial active attempt %d: %w", created.ID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Attempt{}, fmt.Errorf("set initial active attempt %d: %w", created.ID, err)
	} else if affected != 1 {
		return Attempt{}, fmt.Errorf("%w: task %q did not accept initial attempt", ErrLifecycleConflict, t.ID)
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, fmt.Errorf("create task %q: %w", t.ID, err)
	}
	return created, nil
}

func (db *DB) CreateAttemptIfOpenAndInactive(a Attempt) (Attempt, error) {
	if a.Lifecycle != "" && a.Lifecycle != AttemptProvisioning {
		return Attempt{}, fmt.Errorf("%w: new active attempts start provisioning", ErrInvalidTransition)
	}
	a.Ordinal = 0
	return db.createAttempt(a, true)
}

func (db *DB) CreateAttempt(a Attempt) (Attempt, error) {
	return db.createAttempt(a, false)
}

func (db *DB) createAttempt(a Attempt, requireInactive bool) (Attempt, error) {
	if a.Lifecycle == "" {
		a.Lifecycle = AttemptProvisioning
	}
	tx, err := db.beginLifecycleTx("create attempt", a.TaskID)
	if err != nil {
		return Attempt{}, fmt.Errorf("begin attempt creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lifecycle TaskLifecycle
	var activeID sql.NullInt64
	if err := tx.QueryRow(`SELECT lifecycle, active_attempt_id FROM task WHERE id = ?`, a.TaskID).Scan(&lifecycle, &activeID); errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, fmt.Errorf("task %q %w", a.TaskID, ErrTaskNotFound)
	} else if err != nil {
		return Attempt{}, fmt.Errorf("read task %q for attempt: %w", a.TaskID, err)
	} else if lifecycle != TaskOpen {
		return Attempt{}, fmt.Errorf("%w: task %q is terminal", ErrInvalidTransition, a.TaskID)
	}
	if requireInactive && isActiveAttempt(a.Lifecycle) && activeID.Valid {
		return Attempt{}, fmt.Errorf("%w: task %q already owns attempt %d", ErrLifecycleConflict, a.TaskID, activeID.Int64)
	}
	created, err := insertAttemptTx(tx, a)
	if err != nil {
		if isSQLiteBusy(err) {
			return Attempt{}, contention("create attempt", a.TaskID, err)
		}
		if isSQLiteConstraint(err) {
			if isActiveAttempt(a.Lifecycle) && activeAttemptExistsTx(tx, a.TaskID) {
				return Attempt{}, fmt.Errorf("%w: task %q already owns an active attempt", ErrLifecycleConflict, a.TaskID)
			}
			return Attempt{}, fmt.Errorf("%w: attempt ordinal for task %q was claimed by another writer", ErrLifecycleConflict, a.TaskID)
		}
		return Attempt{}, fmt.Errorf("create attempt %d: %w", a.ID, err)
	}
	if isActiveAttempt(a.Lifecycle) {
		result, err := tx.Exec(`UPDATE task SET active_attempt_id = ? WHERE id = ? AND lifecycle = 'open' AND (active_attempt_id IS NULL OR active_attempt_id = ?)`, created.ID, created.TaskID, created.ID)
		if err != nil {
			return Attempt{}, fmt.Errorf("set active attempt %d: %w", created.ID, err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return Attempt{}, fmt.Errorf("set active attempt %d: %w", created.ID, err)
		} else if affected != 1 {
			return Attempt{}, fmt.Errorf("%w: task %q did not accept attempt %d", ErrLifecycleConflict, created.TaskID, created.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, fmt.Errorf("create attempt for task %q: %w", a.TaskID, err)
	}
	return created, nil
}

func insertAttemptTx(tx *sql.Tx, a Attempt) (Attempt, error) {
	if a.Lifecycle == "" {
		a.Lifecycle = AttemptProvisioning
	}
	if a.Ordinal == 0 {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(ordinal), 0) + 1 FROM attempt WHERE task_id = ?`, a.TaskID).Scan(&a.Ordinal); err != nil {
			return Attempt{}, fmt.Errorf("next attempt ordinal for task %q: %w", a.TaskID, err)
		}
	}
	args := attemptValues(a)
	if a.ID == 0 {
		args = args[1:]
		query := `INSERT INTO attempt (` + strings.Join(attemptColumnNames[1:], ", ") + `) VALUES (` + placeholders(len(attemptColumnNames)-1) + `)`
		result, err := tx.Exec(query, args...)
		if err != nil {
			return Attempt{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return Attempt{}, fmt.Errorf("read new attempt ID: %w", err)
		}
		a.ID = id
		return a, nil
	}
	if _, err := tx.Exec(`INSERT INTO attempt (`+attemptColumns+`) VALUES (`+placeholders(len(attemptColumnNames))+`)`, args...); err != nil {
		return Attempt{}, err
	}
	return a, nil
}

func activeAttemptExistsTx(tx *sql.Tx, taskID string) bool {
	var one int
	return tx.QueryRow(`SELECT 1 FROM attempt WHERE task_id = ? AND lifecycle IN ('provisioning', 'running') LIMIT 1`, taskID).Scan(&one) == nil
}

func contention(operation, taskID string, err error) error {
	return fmt.Errorf("%w: %s for task %q lost the database write lock, retry it: %w", ErrContention, operation, taskID, err)
}

func (db *DB) beginLifecycleTx(operation, taskID string) (*sql.Tx, error) {
	tx, err := db.sql.Begin()
	if isSQLiteBusy(err) {
		return nil, contention(operation, taskID, err)
	}
	return tx, err
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}

func isSQLiteConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

func (db *DB) ReadAttempt(id int64) (Attempt, bool, error) {
	row := db.sql.QueryRow(`SELECT `+attemptColumns+` FROM attempt WHERE id = ?`, id)
	a, err := scanAttempt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, false, nil
	}
	if err != nil {
		return Attempt{}, false, fmt.Errorf("read attempt %d: %w", id, err)
	}
	return a, true, nil
}

func (db *DB) ReadActiveAttempt(taskID string) (Attempt, bool, error) {
	row := db.sql.QueryRow(`SELECT `+attemptColumns+` FROM attempt
		WHERE id = (SELECT active_attempt_id FROM task WHERE id = ?)`, taskID)
	a, err := scanAttempt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, false, nil
	}
	if err != nil {
		return Attempt{}, false, fmt.Errorf("read active attempt for task %q: %w", taskID, err)
	}
	return a, true, nil
}

func (db *DB) ListAttempts(taskID string) ([]Attempt, error) {
	rows, err := db.sql.Query(`SELECT `+attemptColumns+` FROM attempt WHERE task_id = ? ORDER BY ordinal`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list attempts for task %q: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var attempts []Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("list attempts for task %q: %w", taskID, err)
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list attempts for task %q: %w", taskID, err)
	}
	return attempts, nil
}

func (db *DB) BeginSend(taskID string, attemptID int64, ownership Herdr, origin SendOrigin, message, createdAt string, usageLimitEpisode ...int64) (SendAttempt, error) {
	if !validSendOrigin(origin) {
		return SendAttempt{}, fmt.Errorf("invalid send origin %q", origin)
	}
	episode := int64(0)
	if len(usageLimitEpisode) > 0 {
		episode = usageLimitEpisode[0]
	}
	tx, err := db.beginLifecycleTx("begin send", taskID)
	if err != nil {
		return SendAttempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var lifecycle TaskLifecycle
	var activeID sql.NullInt64
	if err := tx.QueryRow(`SELECT lifecycle, active_attempt_id FROM task WHERE id = ?`, taskID).Scan(&lifecycle, &activeID); errors.Is(err, sql.ErrNoRows) {
		return SendAttempt{}, fmt.Errorf("task %q: %w", taskID, ErrSendOwnershipConflict)
	} else if err != nil {
		return SendAttempt{}, fmt.Errorf("read task %q before send: %w", taskID, err)
	}
	if lifecycle != TaskOpen || !activeID.Valid || activeID.Int64 != attemptID {
		return SendAttempt{}, fmt.Errorf("task %q does not own running attempt %d: %w", taskID, attemptID, ErrSendOwnershipConflict)
	}
	var session, workspaceID, tabID, paneID string
	var attemptLifecycle AttemptLifecycle
	err = tx.QueryRow(`SELECT lifecycle, herdr_session, herdr_workspace_id, herdr_tab_id, herdr_pane_id
		FROM attempt WHERE id = ? AND task_id = ?`, attemptID, taskID).Scan(&attemptLifecycle, &session, &workspaceID, &tabID, &paneID)
	if errors.Is(err, sql.ErrNoRows) || attemptLifecycle != AttemptRunning || session != ownership.Session || workspaceID != ownership.WorkspaceID || tabID != ownership.TabID || paneID != ownership.PaneID {
		return SendAttempt{}, fmt.Errorf("attempt %d ownership changed: %w", attemptID, ErrSendOwnershipConflict)
	}
	result, err := tx.Exec(`INSERT INTO send_attempt (task_id, attempt_id, origin, message, state, created_at, usage_limit_episode)
		VALUES (?, ?, ?, ?, 'pending', ?, ?)`, taskID, attemptID, origin, message, createdAt, episode)
	if err != nil {
		if isSQLiteConstraint(err) {
			return SendAttempt{}, fmt.Errorf("attempt %d: %w", attemptID, ErrSendInFlight)
		}
		return SendAttempt{}, fmt.Errorf("begin send for task %q: %w", taskID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SendAttempt{}, fmt.Errorf("read send ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SendAttempt{}, fmt.Errorf("begin send for task %q: %w", taskID, err)
	}
	return SendAttempt{ID: id, TaskID: taskID, AttemptID: attemptID, Origin: origin, Message: message, State: SendPending, CreatedAt: createdAt, UsageLimitEpisode: episode}, nil
}

func (db *DB) FinalizeSend(id int64, taskID string, attemptID int64, next SendState, reasonCode, finalizedAt string) (SendAttempt, error) {
	if !validSendTerminalState(next) {
		return SendAttempt{}, fmt.Errorf("send %d cannot become %s: %w", id, next, ErrInvalidSendTransition)
	}
	tx, err := db.beginLifecycleTx("finalize send", taskID)
	if err != nil {
		return SendAttempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	send, found, err := readSendTx(tx, id)
	if err != nil {
		return SendAttempt{}, err
	}
	if !found {
		return SendAttempt{}, fmt.Errorf("send %d: %w", id, ErrSendOwnershipConflict)
	}
	if send.TaskID != taskID || send.AttemptID != attemptID {
		return SendAttempt{}, fmt.Errorf("send %d belongs to task %q attempt %d: %w", id, send.TaskID, send.AttemptID, ErrSendOwnershipConflict)
	}
	if send.State != SendPending {
		if send.State == next {
			return send, nil
		}
		return SendAttempt{}, fmt.Errorf("send %d is already %s: %w", id, send.State, ErrInvalidSendTransition)
	}
	result, err := tx.Exec(`UPDATE send_attempt SET state = ?, reason_code = ?, finalized_at = ?
		WHERE id = ? AND task_id = ? AND attempt_id = ? AND state = 'pending'`, next, reasonCode, finalizedAt, id, taskID, attemptID)
	if err != nil {
		return SendAttempt{}, fmt.Errorf("finalize send %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SendAttempt{}, fmt.Errorf("finalize send %d: %w", id, err)
	}
	if affected != 1 {
		return SendAttempt{}, fmt.Errorf("send %d changed while finalizing: %w", id, ErrSendOwnershipConflict)
	}
	if err := tx.Commit(); err != nil {
		return SendAttempt{}, fmt.Errorf("finalize send %d: %w", id, err)
	}
	send.State, send.ReasonCode, send.FinalizedAt = next, reasonCode, finalizedAt
	return send, nil
}

func (db *DB) NormalizePendingSends(taskID, reasonCode, finalizedAt string) (int64, error) {
	result, err := db.sql.Exec(`UPDATE send_attempt SET state = 'uncertain', reason_code = ?, finalized_at = ?
		WHERE task_id = ? AND state = 'pending'`, reasonCode, finalizedAt, taskID)
	if err != nil {
		return 0, fmt.Errorf("normalize pending sends for task %q: %w", taskID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("normalize pending sends for task %q: %w", taskID, err)
	}
	return affected, nil
}

func (db *DB) NormalizePendingSend(id int64, taskID string, attemptID int64, reasonCode, finalizedAt string) (SendAttempt, bool, error) {
	tx, err := db.beginLifecycleTx("normalize send", taskID)
	if err != nil {
		return SendAttempt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	send, found, err := readSendTx(tx, id)
	if err != nil {
		return SendAttempt{}, false, err
	}
	if !found {
		return SendAttempt{}, false, fmt.Errorf("send %d: %w", id, ErrSendOwnershipConflict)
	}
	if send.TaskID != taskID || send.AttemptID != attemptID {
		return SendAttempt{}, false, fmt.Errorf("send %d belongs to task %q attempt %d: %w", id, send.TaskID, send.AttemptID, ErrSendOwnershipConflict)
	}
	if send.State != SendPending {
		return send, false, nil
	}
	result, err := tx.Exec(`UPDATE send_attempt SET state = 'uncertain', reason_code = ?, finalized_at = ?
		WHERE id = ? AND task_id = ? AND attempt_id = ? AND state = 'pending'`, reasonCode, finalizedAt, id, taskID, attemptID)
	if err != nil {
		return SendAttempt{}, false, fmt.Errorf("normalize send %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SendAttempt{}, false, fmt.Errorf("normalize send %d: %w", id, err)
	}
	if affected != 1 {
		return SendAttempt{}, false, fmt.Errorf("send %d changed while normalizing: %w", id, ErrSendOwnershipConflict)
	}
	if err := tx.Commit(); err != nil {
		return SendAttempt{}, false, fmt.Errorf("normalize send %d: %w", id, err)
	}
	send.State, send.ReasonCode, send.FinalizedAt = SendUncertain, reasonCode, finalizedAt
	return send, true, nil
}

func (db *DB) ImportLegacySend(taskID string, attemptID int64, message, createdAt, finalizedAt string) error {
	if message == "" {
		return nil
	}
	if createdAt == "" {
		createdAt = finalizedAt
	}
	tx, err := db.beginLifecycleTx("import legacy send", taskID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`INSERT INTO send_attempt (task_id, attempt_id, origin, message, state, reason_code, created_at, finalized_at)
		SELECT ?, ?, 'legacy-undelivered', ?, 'uncertain', 'legacy-undelivered-trace', ?, ?
		WHERE EXISTS (SELECT 1 FROM task WHERE id = ?)
		AND EXISTS (SELECT 1 FROM attempt WHERE id = ? AND task_id = ?)
		AND NOT EXISTS (SELECT 1 FROM send_attempt WHERE task_id = ? AND attempt_id = ? AND origin = 'legacy-undelivered' AND message = ? AND created_at = ?)`,
		taskID, attemptID, message, createdAt, finalizedAt, taskID, attemptID, taskID, taskID, attemptID, message, createdAt)
	if err != nil {
		return fmt.Errorf("import legacy send for task %q: %w", taskID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("import legacy send for task %q: %w", taskID, err)
	}
	if affected == 0 {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM attempt WHERE id = ? AND task_id = ?`, attemptID, taskID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("attempt %d for task %q: %w", attemptID, taskID, ErrSendOwnershipConflict)
		} else if err != nil {
			return fmt.Errorf("check imported attempt %d: %w", attemptID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("import legacy send for task %q: %w", taskID, err)
	}
	return nil
}

func (db *DB) ReadSend(id int64) (SendAttempt, bool, error) {
	send, found, err := readSendRow(db.sql.QueryRow(`SELECT id, task_id, attempt_id, origin, message, state, reason_code, created_at, finalized_at, usage_limit_episode
		FROM send_attempt WHERE id = ?`, id))
	if err != nil {
		return SendAttempt{}, false, err
	}
	return send, found, nil
}

func (db *DB) ReadSendMetadata(id int64) (SendAttempt, bool, error) {
	send, found, err := readSendMetadataRow(db.sql.QueryRow(`SELECT id, task_id, attempt_id, origin, state, reason_code, created_at, finalized_at, usage_limit_episode
		FROM send_attempt WHERE id = ?`, id))
	if err != nil {
		return SendAttempt{}, false, err
	}
	return send, found, nil
}

func (db *DB) ListSends(taskID string) ([]SendAttempt, error) {
	rows, err := db.sql.Query(`SELECT id, task_id, attempt_id, origin, message, state, reason_code, created_at, finalized_at, usage_limit_episode
		FROM send_attempt WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list sends for task %q: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var sends []SendAttempt
	for rows.Next() {
		send, err := scanSend(rows)
		if err != nil {
			return nil, fmt.Errorf("list sends for task %q: %w", taskID, err)
		}
		sends = append(sends, send)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sends for task %q: %w", taskID, err)
	}
	return sends, nil
}

func (db *DB) LatestSend(taskID string, attemptID int64, origins ...SendOrigin) (SendAttempt, bool, error) {
	args := []any{taskID, attemptID}
	query := `SELECT id, task_id, attempt_id, origin, message, state, reason_code, created_at, finalized_at, usage_limit_episode
		FROM send_attempt WHERE task_id = ? AND attempt_id = ?`
	if len(origins) > 0 {
		query += ` AND origin IN (` + placeholders(len(origins)) + `)`
		for _, origin := range origins {
			args = append(args, origin)
		}
	}
	query += ` ORDER BY id DESC LIMIT 1`
	send, found, err := readSendRow(db.sql.QueryRow(query, args...))
	if err != nil {
		return SendAttempt{}, false, err
	}
	return send, found, nil
}

func (db *DB) PendingSend(taskID string, attemptID int64) (SendAttempt, bool, error) {
	send, found, err := readSendMetadataRow(db.sql.QueryRow(`SELECT id, task_id, attempt_id, origin, state, reason_code, created_at, finalized_at, usage_limit_episode
		FROM send_attempt WHERE task_id = ? AND attempt_id = ? AND state = 'pending'`, taskID, attemptID))
	if err != nil {
		return SendAttempt{}, false, err
	}
	return send, found, nil
}

func (db *DB) LatestSendMetadata(taskID string, attemptID int64, origins ...SendOrigin) (SendAttempt, bool, error) {
	return db.latestSendMetadata(taskID, attemptID, true, origins...)
}

func LatestSendMetadataReadOnly(homeDir string, taskID string, attemptID int64, origins ...SendOrigin) (SendAttempt, bool, error) {
	db, current, err := openReadOnlyForLifecycle(homeDir)
	if err != nil {
		return SendAttempt{}, false, err
	}
	defer func() { _ = db.Close() }()
	if current < sendSchemaVersion {
		return SendAttempt{}, false, nil
	}
	return db.latestSendMetadata(taskID, attemptID, current > sendSchemaVersion, origins...)
}

func (db *DB) latestSendMetadata(taskID string, attemptID int64, includeEpisode bool, origins ...SendOrigin) (SendAttempt, bool, error) {
	args := []any{taskID, attemptID}
	columns := `id, task_id, attempt_id, origin, state, reason_code, created_at, finalized_at`
	if includeEpisode {
		columns += `, usage_limit_episode`
	}
	query := `SELECT ` + columns + `
		FROM send_attempt WHERE task_id = ? AND attempt_id = ?`
	if len(origins) > 0 {
		query += ` AND origin IN (` + placeholders(len(origins)) + `)`
		for _, origin := range origins {
			args = append(args, origin)
		}
	}
	query += ` ORDER BY id DESC LIMIT 1`
	var send SendAttempt
	var found bool
	var err error
	if includeEpisode {
		send, found, err = readSendMetadataRow(db.sql.QueryRow(query, args...))
	} else {
		send, found, err = readSendMetadataRowBeforeEpisode(db.sql.QueryRow(query, args...))
	}
	if err != nil {
		return SendAttempt{}, false, err
	}
	return send, found, nil
}

func validSendOrigin(origin SendOrigin) bool {
	switch origin {
	case SendOriginOperator, SendOriginUsageLimitResume, SendOriginLegacyUndelivered:
		return true
	default:
		return false
	}
}

func validSendTerminalState(state SendState) bool {
	return state == SendNotSubmitted || state == SendSubmitted || state == SendUncertain
}

type sendScanner interface {
	Scan(...any) error
}

func readSendTx(tx *sql.Tx, id int64) (SendAttempt, bool, error) {
	return readSendRow(tx.QueryRow(`SELECT id, task_id, attempt_id, origin, message, state, reason_code, created_at, finalized_at, usage_limit_episode
		FROM send_attempt WHERE id = ?`, id))
}

func readSendMetadataRow(row sendScanner) (SendAttempt, bool, error) {
	var send SendAttempt
	err := row.Scan(&send.ID, &send.TaskID, &send.AttemptID, &send.Origin, &send.State, &send.ReasonCode, &send.CreatedAt, &send.FinalizedAt, &send.UsageLimitEpisode)
	if errors.Is(err, sql.ErrNoRows) {
		return SendAttempt{}, false, nil
	}
	if err != nil {
		return SendAttempt{}, false, fmt.Errorf("read send metadata: %w", err)
	}
	return send, true, nil
}

func readSendMetadataRowBeforeEpisode(row sendScanner) (SendAttempt, bool, error) {
	var send SendAttempt
	err := row.Scan(&send.ID, &send.TaskID, &send.AttemptID, &send.Origin, &send.State, &send.ReasonCode, &send.CreatedAt, &send.FinalizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SendAttempt{}, false, nil
	}
	if err != nil {
		return SendAttempt{}, false, fmt.Errorf("read send metadata: %w", err)
	}
	return send, true, nil
}

func readSendRow(row sendScanner) (SendAttempt, bool, error) {
	send, err := scanSend(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SendAttempt{}, false, nil
	}
	if err != nil {
		return SendAttempt{}, false, fmt.Errorf("read send: %w", err)
	}
	return send, true, nil
}

func scanSend(row sendScanner) (SendAttempt, error) {
	var send SendAttempt
	err := row.Scan(&send.ID, &send.TaskID, &send.AttemptID, &send.Origin, &send.Message, &send.State, &send.ReasonCode, &send.CreatedAt, &send.FinalizedAt, &send.UsageLimitEpisode)
	return send, err
}

func (db *DB) UpdateAttempt(a Attempt) error {
	_, err := db.sql.Exec(`UPDATE attempt SET task_id = ?, ordinal = ?, lifecycle = ?,
		worktree = ?, lease_id = ?, herdr_session = ?, herdr_workspace_id = ?, herdr_tab_id = ?, herdr_pane_id = ?,
		created_at = ?, pane_started_at = ?, launch_submitted_at = ?, launch_confirmed_at = ?, status_changed_at = ?, status_changed_for = ?, done_verified = ?,
		last_report_state = ?, last_report_note = ?, send_undelivered_message = ?, send_undelivered_at = ?,
		parked_fired_for = ?, usage_limit_retry_at = ?, usage_limit_attempts = ?, teardown_terminal_attempt = ?, teardown_disposition = ?,
		teardown_herdr_state = ?, teardown_worktree_state = ?, teardown_completion_state = ?, usage_limit_episode = ?, usage_limit_stuck_episode = ? WHERE id = ?`,
		a.TaskID, a.Ordinal, a.Lifecycle, a.Worktree, a.LeaseID,
		a.Herdr.Session, a.Herdr.WorkspaceID, a.Herdr.TabID, a.Herdr.PaneID, a.CreatedAt, a.PaneStartedAt,
		a.LaunchSubmittedAt, a.LaunchConfirmedAt,
		a.StatusChangedAt, a.StatusChangedFor, a.DoneVerified, a.LastReportState, a.LastReportNote,
		a.SendUndeliveredMessage, a.SendUndeliveredAt, a.ParkedFiredFor, a.UsageLimitRetryAt, a.UsageLimitAttempts,
		a.TeardownTerminalAttempt, a.TeardownDisposition, a.TeardownHerdrState, a.TeardownWorktreeState, a.TeardownCompletionState,
		a.UsageLimitEpisode, a.UsageLimitStuckEpisode, a.ID)
	if err != nil {
		return fmt.Errorf("update attempt %d: %w", a.ID, err)
	}
	return nil
}

func (db *DB) TransitionAttempt(id int64, from, to AttemptLifecycle) error {
	if !validAttemptTransition(from, to) {
		return fmt.Errorf("%w: attempt %s -> %s", ErrInvalidTransition, from, to)
	}
	tx, err := db.beginLifecycleTx("transition attempt", fmt.Sprintf("attempt:%d", id))
	if err != nil {
		return fmt.Errorf("begin attempt transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var current AttemptLifecycle
	var taskID string
	var launchConfirmedAt string
	if err := tx.QueryRow(`SELECT task_id, lifecycle, launch_confirmed_at FROM attempt WHERE id = ?`, id).Scan(&taskID, &current, &launchConfirmedAt); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("attempt %d %w", id, ErrTaskNotFound)
	} else if err != nil {
		return fmt.Errorf("read attempt %d: %w", id, err)
	}
	if current != from {
		return fmt.Errorf("%w: attempt %d is already %s", ErrLifecycleConflict, id, current)
	}
	if to == AttemptRunning && launchConfirmedAt == "" {
		return fmt.Errorf("%w: attempt launch is not confirmed", ErrInvalidTransition)
	}
	result, err := tx.Exec(`UPDATE attempt SET lifecycle = ? WHERE id = ? AND task_id = ? AND lifecycle = ?`, to, id, taskID, from)
	if err != nil {
		return fmt.Errorf("transition attempt %d: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("transition attempt %d: %w", id, err)
	} else if affected != 1 {
		return fmt.Errorf("%w: attempt %d changed while transitioning", ErrLifecycleConflict, id)
	}
	if isActiveAttempt(to) {
		result, err := tx.Exec(`UPDATE task SET active_attempt_id = ? WHERE id = ? AND lifecycle = 'open' AND (active_attempt_id IS NULL OR active_attempt_id = ?)`, id, taskID, id)
		if err != nil {
			return fmt.Errorf("set active attempt %d: %w", id, err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("set active attempt %d: %w", id, err)
		} else if affected != 1 {
			return fmt.Errorf("%w: task %q no longer owns attempt %d", ErrOwnershipConflict, taskID, id)
		}
	} else {
		result, err := tx.Exec(`UPDATE task SET active_attempt_id = NULL WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?`, taskID, id)
		if err != nil {
			return fmt.Errorf("clear active attempt %d: %w", id, err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("clear active attempt %d: %w", id, err)
		} else if affected != 1 {
			return fmt.Errorf("%w: task %q no longer owns attempt %d", ErrOwnershipConflict, taskID, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("transition attempt %d: %w", id, err)
	}
	return nil
}

func (db *DB) SetActiveAttempt(taskID string, attemptID int64) error {
	result, err := db.sql.Exec(`UPDATE task SET active_attempt_id = ?
		WHERE id = ? AND lifecycle = 'open' AND active_attempt_id IS NULL
		AND EXISTS (SELECT 1 FROM attempt WHERE id = ? AND task_id = ? AND lifecycle IN ('provisioning', 'running'))`, attemptID, taskID, attemptID, taskID)
	if err != nil {
		return fmt.Errorf("set active attempt %d: %w", attemptID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("set active attempt %d: %w", attemptID, err)
	} else if affected != 1 {
		var attemptTask string
		var lifecycle AttemptLifecycle
		if err := db.sql.QueryRow(`SELECT task_id, lifecycle FROM attempt WHERE id = ?`, attemptID).Scan(&attemptTask, &lifecycle); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: attempt %d cannot be active for task %q", ErrInvalidTransition, attemptID, taskID)
		} else if err != nil {
			return fmt.Errorf("read attempt %d after active conflict: %w", attemptID, err)
		}
		if attemptTask != taskID || !isActiveAttempt(lifecycle) {
			return fmt.Errorf("%w: attempt %d cannot be active for task %q", ErrInvalidTransition, attemptID, taskID)
		}
		return fmt.Errorf("%w: task %q already has another active attempt", ErrLifecycleConflict, taskID)
	}
	return nil
}

func (db *DB) TransitionTask(id string, from, to TaskLifecycle) error {
	if !validTaskTransition(from, to) {
		return fmt.Errorf("%w: task %s -> %s", ErrInvalidTransition, from, to)
	}
	result, err := db.sql.Exec(`UPDATE task SET lifecycle = ?, active_attempt_id = CASE WHEN ? = 'terminal' THEN NULL ELSE active_attempt_id END
		WHERE id = ? AND lifecycle = ? AND active_attempt_id IS NULL`, to, to, id, from)
	if err != nil {
		return fmt.Errorf("transition task %q: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("transition task %q: %w", id, err)
	} else if affected != 1 {
		var current TaskLifecycle
		var activeID sql.NullInt64
		if err := db.sql.QueryRow(`SELECT lifecycle, active_attempt_id FROM task WHERE id = ?`, id).Scan(&current, &activeID); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %q %w", id, ErrTaskNotFound)
		} else if err != nil {
			return fmt.Errorf("read task %q after transition conflict: %w", id, err)
		}
		if current == from && !activeID.Valid {
			return fmt.Errorf("%w: task %q changed while transitioning", ErrLifecycleConflict, id)
		}
		return fmt.Errorf("%w: task %q is %s", ErrLifecycleConflict, id, current)
	}
	return nil
}

func validTaskTransition(from, to TaskLifecycle) bool {
	return from == TaskOpen && to == TaskTerminal
}

func (db *DB) ReopenTask(taskID string, a Attempt) (Attempt, error) {
	if a.Lifecycle != AttemptProvisioning {
		return Attempt{}, fmt.Errorf("%w: task reopen requires a provisioning attempt", ErrInvalidTransition)
	}
	a.TaskID = taskID
	a.ID = 0
	a.Ordinal = 0
	tx, err := db.beginLifecycleTx("reopen task", taskID)
	if err != nil {
		return Attempt{}, fmt.Errorf("begin task reopen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lifecycle TaskLifecycle
	if err := tx.QueryRow(`SELECT lifecycle FROM task WHERE id = ?`, taskID).Scan(&lifecycle); errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, fmt.Errorf("task %q %w", taskID, ErrTaskNotFound)
	} else if err != nil {
		return Attempt{}, fmt.Errorf("read task %q for reopen: %w", taskID, err)
	} else if lifecycle != TaskTerminal {
		return Attempt{}, fmt.Errorf("%w: task %q is %s", ErrLifecycleConflict, taskID, lifecycle)
	}
	result, err := insertAttemptTx(tx, a)
	if err != nil {
		if isSQLiteBusy(err) {
			return Attempt{}, contention("reopen task", taskID, err)
		}
		if isSQLiteConstraint(err) {
			return Attempt{}, fmt.Errorf("%w: task %q is being reopened by another writer", ErrLifecycleConflict, taskID)
		}
		return Attempt{}, fmt.Errorf("create reopened attempt for task %q: %w", taskID, err)
	}
	a = result
	updated, err := tx.Exec(`UPDATE task SET lifecycle = 'open', active_attempt_id = ? WHERE id = ? AND lifecycle = 'terminal'`, a.ID, taskID)
	if err != nil {
		return Attempt{}, fmt.Errorf("reopen task %q: %w", taskID, err)
	}
	if affected, err := updated.RowsAffected(); err != nil {
		return Attempt{}, fmt.Errorf("reopen task %q: %w", taskID, err)
	} else if affected != 1 {
		return Attempt{}, fmt.Errorf("%w: task %q changed while reopening", ErrLifecycleConflict, taskID)
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, fmt.Errorf("reopen task %q: %w", taskID, err)
	}
	return a, nil
}

func (db *DB) PromoteTask(taskID string, scoutAttemptID int64, scoutFrom AttemptLifecycle, ship Attempt) (Attempt, error) {
	if !validAttemptTransition(scoutFrom, AttemptCompleted) {
		return Attempt{}, fmt.Errorf("%w: scout attempt %s cannot be completed", ErrInvalidTransition, scoutFrom)
	}
	if ship.Lifecycle != AttemptProvisioning {
		return Attempt{}, fmt.Errorf("%w: promoted attempt must start provisioning", ErrInvalidTransition)
	}
	ship.TaskID = taskID
	ship.ID = 0
	ship.Ordinal = 0
	tx, err := db.beginLifecycleTx("promote task", taskID)
	if err != nil {
		return Attempt{}, fmt.Errorf("begin task promotion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lifecycle TaskLifecycle
	var kind string
	var activeID sql.NullInt64
	if err := tx.QueryRow(`SELECT lifecycle, kind, active_attempt_id FROM task WHERE id = ?`, taskID).Scan(&lifecycle, &kind, &activeID); errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, fmt.Errorf("task %q %w", taskID, ErrTaskNotFound)
	} else if err != nil {
		return Attempt{}, fmt.Errorf("read task %q for promotion: %w", taskID, err)
	}
	if lifecycle != TaskOpen || kind != KindScout || !activeID.Valid || activeID.Int64 != scoutAttemptID {
		return Attempt{}, fmt.Errorf("%w: task %q no longer owns scout attempt %d", ErrLifecycleConflict, taskID, scoutAttemptID)
	}
	result, err := tx.Exec(`UPDATE attempt SET lifecycle = 'completed' WHERE id = ? AND task_id = ? AND lifecycle = ?`, scoutAttemptID, taskID, scoutFrom)
	if err != nil {
		return Attempt{}, fmt.Errorf("complete scout attempt %d: %w", scoutAttemptID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Attempt{}, fmt.Errorf("complete scout attempt %d: %w", scoutAttemptID, err)
	} else if affected != 1 {
		return Attempt{}, fmt.Errorf("%w: scout attempt %d changed during promotion", ErrLifecycleConflict, scoutAttemptID)
	}
	created, err := insertAttemptTx(tx, ship)
	if err != nil {
		if isSQLiteBusy(err) {
			return Attempt{}, contention("promote task", taskID, err)
		}
		if isSQLiteConstraint(err) {
			return Attempt{}, fmt.Errorf("%w: task %q is being promoted by another writer", ErrLifecycleConflict, taskID)
		}
		return Attempt{}, fmt.Errorf("create ship attempt for task %q: %w", taskID, err)
	}
	result, err = tx.Exec(`UPDATE task SET kind = ?, delivered_at = '', delivered_reason = '', active_attempt_id = ?
		WHERE id = ? AND lifecycle = 'open' AND kind = ? AND active_attempt_id = ?`, KindShip, created.ID, taskID, KindScout, scoutAttemptID)
	if err != nil {
		return Attempt{}, fmt.Errorf("take ship ownership for task %q: %w", taskID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Attempt{}, fmt.Errorf("take ship ownership for task %q: %w", taskID, err)
	} else if affected != 1 {
		return Attempt{}, fmt.Errorf("%w: task %q changed while promoting", ErrLifecycleConflict, taskID)
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, fmt.Errorf("promote task %q: %w", taskID, err)
	}
	return created, nil
}

func (db *DB) TerminalizeTaskAndAttempt(taskID string, attemptID int64, attemptFrom, attemptTo AttemptLifecycle) error {
	if !validAttemptTransition(attemptFrom, attemptTo) || isActiveAttempt(attemptTo) {
		return fmt.Errorf("%w: attempt %s -> %s", ErrInvalidTransition, attemptFrom, attemptTo)
	}
	tx, err := db.beginLifecycleTx("terminalize task", taskID)
	if err != nil {
		return fmt.Errorf("begin task terminalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE attempt SET lifecycle = ? WHERE id = ? AND task_id = ? AND lifecycle = ?`, attemptTo, attemptID, taskID, attemptFrom)
	if err != nil {
		return fmt.Errorf("terminalize attempt %d: %w", attemptID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("terminalize attempt %d: %w", attemptID, err)
	} else if affected != 1 {
		return fmt.Errorf("%w: attempt %d changed during terminalization", ErrLifecycleConflict, attemptID)
	}
	result, err = tx.Exec(`UPDATE task SET lifecycle = 'terminal', active_attempt_id = NULL
		WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?`, taskID, attemptID)
	if err != nil {
		return fmt.Errorf("terminalize task %q: %w", taskID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("terminalize task %q: %w", taskID, err)
	} else if affected != 1 {
		return fmt.Errorf("%w: task %q no longer owns attempt %d", ErrLifecycleConflict, taskID, attemptID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("terminalize task %q: %w", taskID, err)
	}
	return nil
}

func (db *DB) SetTaskPR(id, pr string) error {
	result, err := db.sql.Exec(`UPDATE task SET pr = ? WHERE id = ?`, pr, id)
	return updateTaskFact(result, err, "set PR", id)
}

func (db *DB) SetTaskKind(id, kind string) error {
	result, err := db.sql.Exec(`UPDATE task SET kind = ? WHERE id = ? AND lifecycle = 'open'`, kind, id)
	return updateTaskFact(result, err, "set kind", id)
}

func (db *DB) SetTaskMergeAnnounced(id string) error {
	result, err := db.sql.Exec(`UPDATE task SET merge_announced = 1 WHERE id = ?`, id)
	return updateTaskFact(result, err, "set merge announcement", id)
}

func (db *DB) SetTaskDelivery(id, deliveredAt, reason string) error {
	result, err := db.sql.Exec(`UPDATE task SET delivered_at = ?, delivered_reason = ? WHERE id = ? AND lifecycle = 'open'`, deliveredAt, reason, id)
	return updateTaskFact(result, err, "set delivery", id)
}

func (db *DB) SetTaskMerge(id, mergedAt string) error {
	result, err := db.sql.Exec(`UPDATE task SET merge_executed = 1, merge_executed_at = ? WHERE id = ? AND lifecycle = 'open'`, mergedAt, id)
	return updateTaskFact(result, err, "set merge", id)
}

func (db *DB) SetTaskReportState(id string, offset int64, digest string, mergeAnnounced bool) error {
	result, err := db.sql.Exec(`UPDATE task SET report_offset = ?, report_digest = ?, merge_announced = merge_announced OR ? WHERE id = ?`, offset, digest, mergeAnnounced, id)
	return updateTaskFact(result, err, "set report state", id)
}

// Distinct from SetTaskReportState: offset and digest here cover what a supervisor has acknowledged,
// never what a watcher has announced. atqamz/hand#267 keeps the two cursors apart because
// internal/watcher depends on report_offset/report_digest meaning announcement alone.
func (db *DB) SetTaskAcknowledgement(id, at, reason string, offset int64, digest string) error {
	result, err := db.sql.Exec(`UPDATE task SET acknowledged_at = ?, acknowledged_reason = ?, acknowledged_offset = ?, acknowledged_digest = ? WHERE id = ?`,
		at, reason, offset, digest, id)
	return updateTaskFact(result, err, "set acknowledgement", id)
}

func (db *DB) SetTaskRepair(id, code, reason string, attemptID int64, observedAt string) error {
	result, err := db.sql.Exec(`UPDATE task SET repair_code = ?, repair_reason = ?, repair_attempt_id = ?, repair_observed_at = ? WHERE id = ?`, code, reason, attemptID, observedAt, id)
	return updateTaskFact(result, err, "set repair", id)
}

func (db *DB) ClearTaskRepair(id, expectedCode string) error {
	result, err := db.sql.Exec(`UPDATE task SET repair_code = '', repair_reason = '', repair_attempt_id = 0, repair_observed_at = '' WHERE id = ? AND repair_code = ?`, id, expectedCode)
	if err != nil {
		return fmt.Errorf("clear repair for task %q: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("clear repair for task %q: %w", id, err)
	} else if affected == 1 {
		return nil
	}
	var current string
	if err := db.sql.QueryRow(`SELECT repair_code FROM task WHERE id = ?`, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %q %w", id, ErrTaskNotFound)
	} else if err != nil {
		return fmt.Errorf("read repair for task %q: %w", id, err)
	}
	return fmt.Errorf("%w: task %q repair code is %q, expected %q", ErrLifecycleConflict, id, current, expectedCode)
}

func updateTaskFact(result sql.Result, err error, operation, id string) error {
	if err != nil {
		return fmt.Errorf("%s for task %q: %w", operation, id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s for task %q: %w", operation, id, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: task %q was not eligible for %s", ErrLifecycleConflict, id, operation)
	}
	return nil
}

func (db *DB) RecordAttemptWorktree(taskID string, attemptID int64, worktree, branch, leaseID string) error {
	result, err := db.sql.Exec(`UPDATE attempt SET worktree = ?, branch = ?, lease_id = ?
		WHERE id = ? AND task_id = ? AND lifecycle = 'provisioning'
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, worktree, branch, leaseID, attemptID, taskID, taskID, attemptID)
	return updateAttemptOwnership(result, err, "record worktree", taskID, attemptID)
}

func (db *DB) ClearAttemptWorktree(taskID string, attemptID int64) error {
	result, err := db.sql.Exec(`UPDATE attempt SET worktree = '', branch = '', lease_id = ''
		WHERE id = ? AND task_id = ? AND lifecycle = 'provisioning'
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, attemptID, taskID, taskID, attemptID)
	return updateAttemptOwnership(result, err, "clear returned worktree", taskID, attemptID)
}

func (db *DB) RecordAttemptHerdr(taskID string, attemptID int64, herdr Herdr, paneStartedAt string) error {
	result, err := db.sql.Exec(`UPDATE attempt SET herdr_session = ?, herdr_workspace_id = ?, herdr_tab_id = ?, herdr_pane_id = ?, pane_started_at = ?
		WHERE id = ? AND task_id = ? AND lifecycle = 'provisioning'
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, herdr.Session, herdr.WorkspaceID, herdr.TabID, herdr.PaneID, paneStartedAt, attemptID, taskID, taskID, attemptID)
	return updateAttemptOwnership(result, err, "record Herdr identity", taskID, attemptID)
}

func (db *DB) ClearAttemptHerdr(taskID string, attemptID int64) error {
	result, err := db.sql.Exec(`UPDATE attempt SET herdr_session = '', herdr_workspace_id = '', herdr_tab_id = '', herdr_pane_id = '', pane_started_at = ''
		WHERE id = ? AND task_id = ? AND lifecycle = 'provisioning'
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, attemptID, taskID, taskID, attemptID)
	return updateAttemptOwnership(result, err, "clear released Herdr identity", taskID, attemptID)
}

func (db *DB) MarkLaunchSubmitted(taskID string, attemptID int64, at string) error {
	result, err := db.sql.Exec(`UPDATE attempt SET launch_submitted_at = CASE WHEN launch_submitted_at = '' THEN ? ELSE launch_submitted_at END
		WHERE id = ? AND task_id = ? AND lifecycle = 'provisioning'
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, at, attemptID, taskID, taskID, attemptID)
	return updateAttemptOwnership(result, err, "record launch submission", taskID, attemptID)
}

func (db *DB) MarkLaunchConfirmed(taskID string, attemptID int64, at string) error {
	result, err := db.sql.Exec(`UPDATE attempt SET launch_confirmed_at = CASE WHEN launch_confirmed_at = '' THEN ? ELSE launch_confirmed_at END
		WHERE id = ? AND task_id = ? AND lifecycle = 'provisioning' AND launch_submitted_at <> ''
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, at, attemptID, taskID, taskID, attemptID)
	return updateAttemptOwnership(result, err, "record launch confirmation", taskID, attemptID)
}

func (db *DB) MarkAttemptRunning(taskID string, attemptID int64) error {
	result, err := db.sql.Exec(`UPDATE attempt SET lifecycle = 'running', status_changed_at = CASE WHEN status_changed_at = '' THEN launch_confirmed_at ELSE status_changed_at END
		WHERE id = ? AND task_id = ? AND lifecycle = 'provisioning' AND launch_confirmed_at <> ''
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, attemptID, taskID, taskID, attemptID)
	if err != nil {
		return fmt.Errorf("record running attempt for attempt %d: %w", attemptID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("record running attempt for attempt %d: %w", attemptID, err)
	}
	if affected == 1 {
		return nil
	}
	var lifecycle AttemptLifecycle
	var launchConfirmedAt string
	var ownerLifecycle TaskLifecycle
	var activeID sql.NullInt64
	err = db.sql.QueryRow(`SELECT attempt.lifecycle, attempt.launch_confirmed_at, task.lifecycle, task.active_attempt_id
		FROM attempt JOIN task ON task.id = attempt.task_id WHERE attempt.id = ? AND attempt.task_id = ?`, attemptID, taskID).Scan(&lifecycle, &launchConfirmedAt, &ownerLifecycle, &activeID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: task %q no longer owns active attempt %d", ErrOwnershipConflict, taskID, attemptID)
	}
	if err != nil {
		return fmt.Errorf("read attempt %d after running conflict: %w", attemptID, err)
	}
	if ownerLifecycle != TaskOpen || !activeID.Valid || activeID.Int64 != attemptID {
		return fmt.Errorf("%w: task %q no longer owns active attempt %d", ErrOwnershipConflict, taskID, attemptID)
	}
	if lifecycle == AttemptProvisioning && launchConfirmedAt == "" {
		return fmt.Errorf("%w: attempt launch is not confirmed", ErrInvalidTransition)
	}
	return fmt.Errorf("%w: attempt %d is already %s", ErrLifecycleConflict, attemptID, lifecycle)
}

func (db *DB) SetAttemptTeardownDecision(taskID string, attemptID int64, terminal AttemptLifecycle, disposition string) error {
	if terminal != AttemptCompleted && terminal != AttemptInterrupted {
		return fmt.Errorf("%w: teardown terminal lifecycle %s", ErrInvalidTransition, terminal)
	}
	result, err := db.sql.Exec(`UPDATE attempt SET teardown_terminal_attempt = ?, teardown_disposition = ?
		WHERE id = ? AND task_id = ? AND lifecycle IN ('provisioning', 'running')
		AND teardown_terminal_attempt = ''
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, terminal, disposition, attemptID, taskID, taskID, attemptID)
	if err != nil {
		return fmt.Errorf("record teardown decision for attempt %d: %w", attemptID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("record teardown decision for attempt %d: %w", attemptID, err)
	} else if affected == 1 {
		return nil
	}
	var current AttemptLifecycle
	var currentDisposition string
	if err := db.sql.QueryRow(`SELECT teardown_terminal_attempt, teardown_disposition FROM attempt WHERE id = ? AND task_id = ?`, attemptID, taskID).Scan(&current, &currentDisposition); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: task %q no longer owns active attempt %d", ErrOwnershipConflict, taskID, attemptID)
	} else if err != nil {
		return fmt.Errorf("read teardown decision for attempt %d: %w", attemptID, err)
	}
	if current == terminal && currentDisposition == disposition {
		return nil
	}
	return fmt.Errorf("%w: teardown decision for attempt %d is already %s/%s", ErrLifecycleConflict, attemptID, current, currentDisposition)
}

func (db *DB) SetAttemptTeardownResourceState(taskID string, attemptID int64, expected AttemptLifecycle, resource, next string) error {
	column := ""
	switch resource {
	case "herdr":
		column = "teardown_herdr_state"
	case "worktree":
		column = "teardown_worktree_state"
	default:
		return fmt.Errorf("%w: unknown teardown resource %q", ErrInvalidTransition, resource)
	}
	if next != TeardownResourceReleasing && next != TeardownResourceReleased && next != TeardownResourceAmbiguous && next != TeardownResourceRetryable && next != TeardownResourceAbandoned {
		return fmt.Errorf("%w: unknown teardown resource state %q", ErrInvalidTransition, next)
	}
	currentAllowed := "''"
	switch next {
	// 'ambiguous' is a predecessor of 'releasing' so that a later proof of ownership can clear the
	// latch an unobservable pool leaves behind; the proof is the caller's job, not this table's.
	case TeardownResourceReleasing:
		currentAllowed = "'', 'retryable', 'ambiguous'"
	case TeardownResourceReleased, TeardownResourceRetryable:
		currentAllowed = "'releasing'"
	case TeardownResourceAmbiguous:
		currentAllowed = "'releasing', 'retryable', ''"
	case TeardownResourceAbandoned:
		currentAllowed = "'ambiguous', 'retryable', ''"
	}
	query := `UPDATE attempt SET ` + column + ` = ? WHERE id = ? AND task_id = ? AND lifecycle = ? AND ` + column + ` IN (` + currentAllowed + `)`
	result, err := db.sql.Exec(query, next, attemptID, taskID, expected)
	if err != nil {
		return fmt.Errorf("record teardown %s state for attempt %d: %w", resource, attemptID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("record teardown %s state for attempt %d: %w", resource, attemptID, err)
	} else if affected == 1 {
		return nil
	}
	var current string
	if err := db.sql.QueryRow(`SELECT `+column+` FROM attempt WHERE id = ? AND task_id = ?`, attemptID, taskID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: task %q no longer owns attempt %d", ErrOwnershipConflict, taskID, attemptID)
	} else if err != nil {
		return fmt.Errorf("read teardown %s state for attempt %d: %w", resource, attemptID, err)
	}
	if current == next {
		return nil
	}
	return fmt.Errorf("%w: teardown %s state for attempt %d is already %q", ErrLifecycleConflict, resource, attemptID, current)
}

func (db *DB) SetAttemptTeardownCompletionState(taskID string, attemptID int64, expected AttemptLifecycle, next string) error {
	if next != TeardownCompletionPending && next != TeardownCompletionAppended {
		return fmt.Errorf("%w: unknown teardown completion state %q", ErrInvalidTransition, next)
	}
	allowed := "''"
	if next == TeardownCompletionAppended {
		allowed = "'pending'"
	}
	result, err := db.sql.Exec(`UPDATE attempt SET teardown_completion_state = ? WHERE id = ? AND task_id = ? AND lifecycle = ? AND teardown_completion_state IN (`+allowed+`)`, next, attemptID, taskID, expected)
	if err != nil {
		return fmt.Errorf("record teardown completion state for attempt %d: %w", attemptID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("record teardown completion state for attempt %d: %w", attemptID, err)
	} else if affected == 1 {
		return nil
	}
	var current string
	if err := db.sql.QueryRow(`SELECT teardown_completion_state FROM attempt WHERE id = ? AND task_id = ?`, attemptID, taskID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: task %q no longer owns attempt %d", ErrOwnershipConflict, taskID, attemptID)
	} else if err != nil {
		return fmt.Errorf("read teardown completion state for attempt %d: %w", attemptID, err)
	}
	if current == next {
		return nil
	}
	return fmt.Errorf("%w: teardown completion state for attempt %d is already %q", ErrLifecycleConflict, attemptID, current)
}

func (db *DB) SetAttemptSendTrace(taskID string, attemptID int64, expected AttemptLifecycle, message, at string) error {
	result, err := db.sql.Exec(`UPDATE attempt SET send_undelivered_message = ?, send_undelivered_at = ?
		WHERE id = ? AND task_id = ? AND lifecycle = ?
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, message, at, attemptID, taskID, expected, taskID, attemptID)
	return updateAttemptOwnership(result, err, "record send trace", taskID, attemptID)
}

func (db *DB) UpdateAttemptObservation(taskID string, attemptID int64, expected AttemptLifecycle, statusChangedAt, statusChangedFor string, doneVerified bool, lastReportState, lastReportNote, parkedFiredFor, usageLimitRetryAt string, usageLimitAttempts int, usageLimitEpisode, usageLimitStuckEpisode int64) error {
	result, err := db.sql.Exec(`UPDATE attempt SET status_changed_at = ?, status_changed_for = ?, done_verified = done_verified OR ?,
		last_report_state = ?, last_report_note = ?, parked_fired_for = ?, usage_limit_retry_at = ?, usage_limit_attempts = ?, usage_limit_episode = ?, usage_limit_stuck_episode = ?
		WHERE id = ? AND task_id = ? AND lifecycle = ?
		AND EXISTS (SELECT 1 FROM task WHERE id = ? AND lifecycle = 'open' AND active_attempt_id = ?)`, statusChangedAt, statusChangedFor, doneVerified, lastReportState, lastReportNote, parkedFiredFor, usageLimitRetryAt, usageLimitAttempts, usageLimitEpisode, usageLimitStuckEpisode, attemptID, taskID, expected, taskID, attemptID)
	return updateAttemptOwnership(result, err, "record watcher observation", taskID, attemptID)
}

func updateAttemptOwnership(result sql.Result, err error, operation, taskID string, attemptID int64) error {
	if err != nil {
		return fmt.Errorf("%s for attempt %d: %w", operation, attemptID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s for attempt %d: %w", operation, attemptID, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: task %q no longer owns active attempt %d for %s", ErrOwnershipConflict, taskID, attemptID, operation)
	}
	return nil
}

func validAttemptTransition(from, to AttemptLifecycle) bool {
	switch from {
	case AttemptProvisioning:
		return to == AttemptRunning || to == AttemptFailed || to == AttemptInterrupted
	case AttemptRunning:
		return to == AttemptCompleted || to == AttemptFailed || to == AttemptInterrupted
	default:
		return false
	}
}

func isActiveAttempt(lifecycle AttemptLifecycle) bool {
	return lifecycle == AttemptProvisioning || lifecycle == AttemptRunning
}

func (db *DB) TaskExists(id string) (bool, error) {
	var one int
	err := db.sql.QueryRow(`SELECT 1 FROM task WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check task %q: %w", id, err)
	}
	return true, nil
}

func (db *DB) DeleteTask(id string) error {
	res, err := db.sql.Exec(`DELETE FROM task WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task %q: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete task %q: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("task %q %w", id, ErrTaskNotFound)
	}
	return nil
}

func (db *DB) ListProjects() ([]Project, error) {
	if db.empty {
		return nil, nil
	}
	rows, err := db.sql.Query(`SELECT name, url, mode, upstream FROM project ORDER BY position, name`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name, &p.URL, &p.Mode, &p.Upstream); err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

func (db *DB) AddProject(p Project) error {
	_, err := db.sql.Exec(`INSERT INTO project (name, url, mode, position, upstream)
		VALUES (?, ?, ?, (SELECT COALESCE(MAX(position), -1) + 1 FROM project), ?)`, p.Name, p.URL, p.Mode, p.Upstream)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("project %q %w", p.Name, ErrProjectExists)
		}
		return fmt.Errorf("add project %q: %w", p.Name, err)
	}
	return nil
}

// SetProjectUpstream reports whether a row was actually updated, the same way
// RemoveProject reports a removal.
func (db *DB) SetProjectUpstream(name, upstream string) (bool, error) {
	res, err := db.sql.Exec(`UPDATE project SET upstream = ? WHERE name = ?`, upstream, name)
	if err != nil {
		return false, fmt.Errorf("set upstream for project %q: %w", name, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set upstream for project %q: %w", name, err)
	}
	return affected > 0, nil
}

// Reports whether the named project's URL was updated.
func (db *DB) SetProjectURL(name, url string) (bool, error) {
	res, err := db.sql.Exec(`UPDATE project SET url = ? WHERE name = ?`, url, name)
	if err != nil {
		return false, fmt.Errorf("set URL for project %q: %w", name, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set URL for project %q: %w", name, err)
	}
	return affected > 0, nil
}

// RemoveProject reports whether a row was actually removed, leaving the
// not-registered wording to the caller that already owns it.
func (db *DB) RemoveProject(name string) (bool, error) {
	res, err := db.sql.Exec(`DELETE FROM project WHERE name = ?`, name)
	if err != nil {
		return false, fmt.Errorf("remove project %q: %w", name, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("remove project %q: %w", name, err)
	}
	return affected > 0, nil
}

const holdColumns = `id, kind, reason, blocked_on, set_at, inferred`

const holdColumnsBeforeInferred = `id, kind, reason, blocked_on, set_at`

func scanHold(row interface{ Scan(...any) error }) (Hold, error) {
	var h Hold
	err := row.Scan(&h.ID, &h.Kind, &h.Reason, &h.BlockedOn, &h.SetAt, &h.Inferred)
	return h, err
}

// SetHold upserts, so an operator narrowing down a reason re-runs the same
// command rather than needing a clear first - SetAt then reads as when the
// hold was last set, not when it was first raised.
func (db *DB) SetHold(h Hold) error {
	_, err := db.sql.Exec(`INSERT INTO hold (`+holdColumns+`)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind, reason = excluded.reason,
			blocked_on = excluded.blocked_on, set_at = excluded.set_at, inferred = excluded.inferred`,
		h.ID, h.Kind, h.Reason, h.BlockedOn, h.SetAt, h.Inferred)
	if err != nil {
		return fmt.Errorf("write hold %q: %w", h.ID, err)
	}
	return nil
}

func (db *DB) SetHoldIfNotOtherKind(h Hold) (bool, error) {
	result, err := db.sql.Exec(`INSERT INTO hold (`+holdColumns+`)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind, reason = excluded.reason,
			blocked_on = excluded.blocked_on, set_at = excluded.set_at, inferred = excluded.inferred
		WHERE hold.kind = excluded.kind`, h.ID, h.Kind, h.Reason, h.BlockedOn, h.SetAt, h.Inferred)
	if err != nil {
		return false, fmt.Errorf("write conditional hold %q: %w", h.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("write conditional hold %q: %w", h.ID, err)
	}
	return affected == 1, nil
}

func (db *DB) ReadHold(id string) (Hold, bool, error) {
	if db.empty {
		return Hold{}, false, nil
	}
	row := db.sql.QueryRow(`SELECT `+holdColumns+` FROM hold WHERE id = ?`, id)
	h, err := scanHold(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Hold{}, false, nil
	}
	if err != nil {
		return Hold{}, false, fmt.Errorf("read hold %q: %w", id, err)
	}
	return h, true, nil
}

func (db *DB) readHoldBeforeInferred(id string) (Hold, bool, error) {
	if db.empty {
		return Hold{}, false, nil
	}
	row := db.sql.QueryRow(`SELECT `+holdColumnsBeforeInferred+` FROM hold WHERE id = ?`, id)
	var h Hold
	err := row.Scan(&h.ID, &h.Kind, &h.Reason, &h.BlockedOn, &h.SetAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Hold{}, false, nil
	}
	if err != nil {
		return Hold{}, false, fmt.Errorf("read hold %q: %w", id, err)
	}
	return h, true, nil
}

// ListHolds surfaces every row, whatever it holds: filtering here on kind, or on BlockedOn
// being consistent with kind, would let a row an external write left inconsistent disappear
// from "what is held" instead of being reported as a hold that needs attention.
func (db *DB) ListHolds() ([]Hold, error) {
	if db.empty {
		return nil, nil
	}
	rows, err := db.sql.Query(`SELECT ` + holdColumns + ` FROM hold ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list holds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var holds []Hold
	for rows.Next() {
		h, err := scanHold(rows)
		if err != nil {
			return nil, fmt.Errorf("list holds: %w", err)
		}
		holds = append(holds, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list holds: %w", err)
	}
	return holds, nil
}

func (db *DB) ClearHold(id string) error {
	res, err := db.sql.Exec(`DELETE FROM hold WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("clear hold %q: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("clear hold %q: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("hold %q %w", id, ErrHoldNotFound)
	}
	return nil
}

func (db *DB) ClearHoldIfKind(id, kind string) (bool, error) {
	result, err := db.sql.Exec(`DELETE FROM hold WHERE id = ? AND kind = ?`, id, kind)
	if err != nil {
		return false, fmt.Errorf("clear conditional hold %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("clear conditional hold %q: %w", id, err)
	}
	return affected == 1, nil
}
