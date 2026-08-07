package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/atqamz/secondhand/internal/atomicfile"
	"github.com/atqamz/secondhand/internal/axi"
	"github.com/atqamz/secondhand/internal/harness"
	"github.com/atqamz/secondhand/internal/home"
	"github.com/spf13/cobra"
)

const (
	settingHarness = "harness"
	settingModel   = "model"
	settingEffort  = "effort"
)

// In the order configuration asks for them: the harness decides which of the other two apply at all.
var workerSettingKeys = []string{settingHarness, settingModel, settingEffort}

const (
	stateConfigured    = "configured"
	stateDetected      = "detected"
	stateNativeDefault = "native-default"
	stateMissing       = "missing"
	// The selected harness takes no such launch flag, so there is nothing to configure - distinct from
	// missing, which is a question still owed an answer.
	stateUnsupported = "unsupported"
	// Applicability is a property of the harness, so it is unknown until one is chosen.
	statePendingHarness = "pending-harness"
)

type workerSetting struct {
	key   string
	state string
	value string
}

var workerSettingFields = []axi.Column[workerSetting]{
	{Name: "key", Value: func(s workerSetting) string { return s.key }},
	{Name: "state", Value: func(s workerSetting) string { return s.state }},
	{Name: "value", Value: func(s workerSetting) string { return orNone(s.value) }},
}

// Every capability column is read off internal/harness rather than restated here: a second table that
// claims to know which harness takes a model flag is one that can disagree with the launch command.
var harnessFields = []axi.Column[string]{
	{Name: "name", Value: func(name string) string { return name }},
	{Name: "installed", Value: func(name string) string { return strconv.FormatBool(onPath(name)) }},
	{Name: "model", Value: func(name string) string { return strconv.FormatBool(harness.SupportsModel(name)) }},
	{Name: "effort", Value: func(name string) string { return strconv.FormatBool(harness.SupportsEffort(name)) }},
}

type workerConfig struct {
	detection harness.Detection
	harness   string
	settings  []workerSetting
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Report effective worker defaults and optional overrides",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			fleetHome, err := home.Resolve()
			if err != nil {
				return asPrecondition(err)
			}
			cfg, err := currentWorkerConfig(fleetHome)
			if err != nil {
				return err
			}

			var doc axi.Doc
			doc.Field("home", fleetHome)
			doc.Field("harness", orNone(cfg.harness))
			appendWorkerConfig(&doc, cfg)
			axi.Table(&doc, "harnesses", harness.Names(), harnessFields)
			doc.Help(workerConfigHelp(cfg)...)
			return doc.Render(cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(newConfigSetCmd())
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Validate and persist one worker default",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			fleetHome, err := home.Resolve()
			if err != nil {
				return asPrecondition(err)
			}
			cfg, err := currentWorkerConfig(fleetHome)
			if err != nil {
				return err
			}
			rel, err := writeWorkerSetting(fleetHome, key, value, cfg.harness)
			if err != nil {
				return err
			}
			cfg = readWorkerConfig(fleetHome, cfg.detection)

			var doc axi.Doc
			doc.Field("result", "set")
			doc.Field("home", fleetHome)
			doc.Field("key", key)
			doc.Field("value", value)
			doc.Field("file", rel)
			doc.Field("harness", orNone(cfg.harness))
			appendWorkerConfig(&doc, cfg)
			help := workerConfigHelp(cfg)
			if len(help) == 0 {
				help = append(help, "Every applicable worker default is configured; run `hand project add <repo-url>` to register a project")
			}
			doc.Help(help...)
			return doc.Render(cmd.OutOrStdout())
		},
	}
}

// The report the session hook and `hand config` both render, so a supervisor rechecking after an answer
// reads the same shape it read at session start.
func appendWorkerConfig(doc *axi.Doc, cfg workerConfig) {
	doc.Int("config_missing", configMissing(cfg))
	axi.Table(doc, "config", cfg.settings, workerSettingFields)
}

func configMissing(cfg workerConfig) int {
	missing := 0
	for _, s := range cfg.settings {
		if s.state == stateMissing {
			missing++
		}
	}
	return missing
}

// An unknown supervisor is the only decision that blocks effective defaults; model and effort can
// stay with the harness's native selection until an operator chooses an override.
func workerConfigHelp(cfg workerConfig) []string {
	if cfg.harness != "" {
		return nil
	}
	return []string{"Ask the operator which harness this fleet's workers should default to, then run `hand config set harness <name>`; `hand config` lists the supported ones and which are installed"}
}

func currentWorkerConfig(fleetHome string) (workerConfig, error) {
	detected, err := harness.DetectCurrent()
	if err != nil {
		return workerConfig{}, err
	}
	return readWorkerConfig(fleetHome, detected), nil
}

// Effective model and effort overrides are read from files keyed to the selected harness, never from a
// bare config/model: a value chosen for one harness is not a default for the next one.
func readWorkerConfig(fleetHome string, detected harness.Detection) workerConfig {
	cfg := workerConfig{detection: detected}
	configured := configDefault(fleetHome, settingHarness, "")
	harnessState := stateMissing
	switch {
	case configured != "":
		cfg.harness = configured
		harnessState = stateConfigured
	case harness.IsSupported(detected.Name):
		cfg.harness = detected.Name
		harnessState = stateDetected
	}
	cfg.settings = []workerSetting{{key: settingHarness, state: harnessState, value: cfg.harness}}
	for _, key := range []string{settingModel, settingEffort} {
		s := workerSetting{key: key}
		switch {
		case cfg.harness == "":
			s.state = statePendingHarness
		case !harnessCarries(key, cfg.harness):
			s.state = stateUnsupported
		default:
			s.value = configDefault(fleetHome, harnessSettingKey(key, cfg.harness), "")
			s.state = stateConfigured
			if s.value == "" {
				s.state = stateNativeDefault
			}
		}
		cfg.settings = append(cfg.settings, s)
	}
	return cfg
}

func workerDefault(fleetHome, key, harnessName string) string {
	return configDefault(fleetHome, harnessSettingKey(key, harnessName), "")
}

func harnessSettingKey(key, harnessName string) string {
	return key + "." + harnessName
}

func harnessCarries(key, harnessName string) bool {
	if key == settingEffort {
		return harness.SupportsEffort(harnessName)
	}
	return harness.SupportsModel(harnessName)
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Harness names are validated against internal/harness, and effort and model are not: hand knows which
// launch flags a harness takes, and a model identifier belongs to the harness's own catalog, which a
// release of hand cannot keep up with.
func writeWorkerSetting(fleetHome, key, value, currentHarness string) (string, error) {
	if !slices.Contains(workerSettingKeys, key) {
		return "", &ExitError{Err: fmt.Errorf("unknown setting %q: want one of %s", key, strings.Join(workerSettingKeys, ", ")), Code: 2}
	}
	if value != strings.TrimSpace(value) || len(strings.Fields(value)) != 1 {
		return "", &ExitError{Err: fmt.Errorf("%s value %q must be one word with no surrounding whitespace", key, value), Code: 2}
	}

	name := key
	if key == settingHarness {
		if !harness.IsSupported(value) {
			return "", &ExitError{Err: fmt.Errorf("harness %q not recognized: want one of %s", value, strings.Join(harness.Names(), ", ")), Code: 2}
		}
	} else {
		if currentHarness == "" {
			return "", &ExitError{Err: fmt.Errorf("current supervisor harness is unknown and no worker harness override is configured; run hand config set harness <name> before setting %s", key), Code: 3}
		}
		if !harnessCarries(key, currentHarness) {
			return "", &ExitError{Err: fmt.Errorf("harness %q takes no %s, so there is nothing to configure", currentHarness, key), Code: 2}
		}
		name = harnessSettingKey(key, currentHarness)
	}

	dir := filepath.Join(fleetHome, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create config: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(dir, name), ".config-", []byte(value+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write config/%s: %w", name, err)
	}
	return filepath.Join("config", name), nil
}

// Moves a bare config/model or config/effort under the harness it was written for, and reports which keys
// moved. Left unkeyed, that value would become the default for whatever harness the home switches to
// next, which is how a claude model identifier reaches an opencode worker.
func migrateWorkerSettings(fleetHome string) ([]string, error) {
	harnessName := configDefault(fleetHome, settingHarness, "")
	if harnessName == "" {
		return nil, nil
	}
	var moved []string
	var errs []error
	for _, key := range []string{settingModel, settingEffort} {
		unkeyed := filepath.Join(fleetHome, "config", key)
		if _, err := os.Stat(unkeyed); err != nil {
			continue
		}
		keyed := filepath.Join(fleetHome, "config", harnessSettingKey(key, harnessName))
		if _, err := os.Stat(keyed); err == nil {
			continue
		}
		if err := os.Rename(unkeyed, keyed); err != nil {
			errs = append(errs, fmt.Errorf("move config/%s under %s: %w", key, harnessName, err))
			continue
		}
		moved = append(moved, key)
	}
	return moved, errors.Join(errs...)
}
