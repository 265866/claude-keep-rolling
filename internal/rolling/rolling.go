// Package rolling holds the host-independent rules for keeping Claude
// five-hour usage windows rolling: settings, account selection and ping timing.
package rolling

import (
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultModel  = "claude-haiku-4-5-20251001"
	DefaultPrompt = "hi"

	// ResetBuffer delays a ping past the reported reset so it lands in the new window.
	ResetBuffer = time.Minute
	// WindowFallback is used when a successful reply carries no reset header.
	WindowFallback = 5*time.Hour + ResetBuffer
	// RetryAfterFailure is the wait before retrying a failed ping. Failed replies
	// reach plugins without headers, so the real reset time is unknown.
	RetryAfterFailure = 15 * time.Minute
	// SkewTolerance and SkewRetry handle a reset reported at or shortly before the ping time.
	SkewTolerance = 10 * time.Minute
	SkewRetry     = 2 * time.Minute

	resetHeader = "Anthropic-Ratelimit-Unified-5h-Reset"
)

// Settings is the plugin's block under plugins.configs.claude-keep-rolling.
type Settings struct {
	Model     string   `yaml:"model" json:"model"`
	Prompt    string   `yaml:"prompt" json:"prompt"`
	SelectAll bool     `yaml:"select_all" json:"select_all"`
	Accounts  []string `yaml:"accounts" json:"accounts"`
}

// ParseSettings reads the plugin config YAML. Missing or blank fields take defaults,
// and an unconfigured plugin rolls every account.
func ParseSettings(raw []byte) (Settings, error) {
	settings := Settings{SelectAll: true}
	if err := yaml.Unmarshal(raw, &settings); err != nil {
		return Settings{}, err
	}
	settings.Model = strings.TrimSpace(settings.Model)
	if settings.Model == "" {
		settings.Model = DefaultModel
	}
	if strings.TrimSpace(settings.Prompt) == "" {
		settings.Prompt = DefaultPrompt
	}
	if settings.Accounts == nil {
		settings.Accounts = []string{}
	}
	return settings, nil
}

// Selected reports whether the account should be kept rolling.
func (s Settings) Selected(accountID string) bool {
	return s.SelectAll || slices.Contains(s.Accounts, accountID)
}

// HostConfigPath returns the CLIProxyAPI config file path the way the host resolves it:
// the -config flag, otherwise config.yaml in the working directory.
func HostConfigPath(args []string, workDir string) string {
	for i, arg := range args {
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || name != "config" {
			continue
		}
		if hasValue {
			value = strings.TrimSpace(value)
		} else if i+1 < len(args) {
			value = strings.TrimSpace(args[i+1])
		}
		if value != "" {
			return value
		}
	}
	return filepath.Join(workDir, "config.yaml")
}

// PluginEnabled reports whether the host config enables plugins and this plugin.
// The host keeps a disabled plugin's library loaded without notifying it, so the
// plugin reads its own enabled flag before acting.
func PluginEnabled(raw []byte, pluginID string) (bool, error) {
	var cfg struct {
		Plugins struct {
			Enabled bool `yaml:"enabled"`
			Configs map[string]struct {
				Enabled bool `yaml:"enabled"`
			} `yaml:"configs"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return false, err
	}
	return cfg.Plugins.Enabled && cfg.Plugins.Configs[pluginID].Enabled, nil
}

// Outcome is the result of one ping.
type Outcome struct {
	At      time.Time
	OK      bool
	ResetAt time.Time
	// CooldownUntil is the host's retry time for the account after a failure.
	CooldownUntil time.Time
	// Manual marks a ping started from the status page. Scheduled is the next
	// ping that was already planned when it started.
	Manual    bool
	Scheduled time.Time
}

// NextPing returns when the account should be pinged again. A failed manual
// ping never delays the ping that was already scheduled.
func NextPing(o Outcome) time.Time {
	if !o.OK {
		next := o.At.Add(RetryAfterFailure)
		if cooled := o.CooldownUntil.Add(ResetBuffer); !o.CooldownUntil.IsZero() && cooled.After(next) {
			next = cooled
		}
		if o.Manual && !o.Scheduled.IsZero() && o.Scheduled.Before(next) {
			next = o.Scheduled
		}
		return next
	}
	if o.ResetAt.After(o.At) {
		return o.ResetAt.Add(ResetBuffer)
	}
	// A reset at or just before the ping time means the local clock runs ahead of
	// Anthropic's and the ping landed in the old window. Retry shortly.
	if !o.ResetAt.IsZero() && o.At.Sub(o.ResetAt) <= SkewTolerance {
		return o.At.Add(SkewRetry)
	}
	return o.At.Add(WindowFallback)
}

// ResetTime extracts the five-hour window reset from Claude response headers.
// The header carries Unix seconds; RFC 3339 is accepted as well.
func ResetTime(headers http.Header) (time.Time, bool) {
	raw := ""
	for key, values := range headers {
		if strings.EqualFold(key, resetHeader) && len(values) > 0 {
			raw = strings.TrimSpace(values[0])
			break
		}
	}
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		return time.Unix(seconds, 0).UTC(), true
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), true
	}
	return time.Time{}, false
}
