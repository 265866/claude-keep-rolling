// Package rolling holds the host-independent rules for keeping Claude
// five-hour usage windows rolling: settings, account selection and ping timing.
package rolling

import (
	"math"
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
	// WindowFallback is used when a successful reply carries no five-hour reset.
	WindowFallback = 5*time.Hour + ResetBuffer
	// RecheckFallback is used when an account that is not rolling has no weekly reset to wait for.
	RecheckFallback = 24 * time.Hour
	// RetryAfterFailure is the wait before retrying a failed ping. Failed replies
	// reach plugins without headers, so the real reset time is unknown.
	RetryAfterFailure = 15 * time.Minute
	// SkewTolerance and SkewRetry handle a reset reported at or shortly before the ping time.
	SkewTolerance = 10 * time.Minute
	SkewRetry     = 2 * time.Minute

	// A reset further ahead than its window plus an hour is not a window reset.
	// CLIProxyAPI caps weekly resets the same way.
	fiveHourMaxAhead = 5*time.Hour + time.Hour
	weeklyMaxAhead   = 7*24*time.Hour + time.Hour

	limitHeaderPrefix = "anthropic-ratelimit-unified-"
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

// Mode is how the plugin treats an account, decided by its last successful ping.
// The zero value means no ping has succeeded yet.
type Mode string

const (
	// ModeRolling pings the account one minute after each five-hour reset.
	ModeRolling Mode = "rolling"
	// ModeNoFiveHour is an account whose replies carry only a weekly limit, such as
	// a verified Ultra account. Pings cannot start a five-hour window, so the
	// account is re-checked once, just after the weekly reset.
	ModeNoFiveHour Mode = "no_five_hour"
	// ModeWeeklyReached is an account that used up its weekly limit. It is not
	// pinged again until the weekly reset.
	ModeWeeklyReached Mode = "weekly_reached"
)

// Limits is what a reply's rate-limit headers say about the account. Only the
// all-models limits count. Model-scoped limits, such as Fable's 7d_oi, are
// ignored because pings use another model.
type Limits struct {
	// FiveHour and Weekly report whether any header for that limit was present.
	FiveHour bool
	Weekly   bool
	// FiveHourReset and WeeklyReset are zero when the header is missing or unreadable.
	FiveHourReset time.Time
	WeeklyReset   time.Time
	// WeeklyRejected reports a weekly status of "rejected".
	WeeklyRejected bool
	// WeeklyUtilization is the used share of the weekly limit, 1 when used up.
	// It is nil when the header is missing or unreadable.
	WeeklyUtilization *float64
}

// WeeklyReached reports whether the weekly limit is used up.
func (l Limits) WeeklyReached() bool {
	return l.WeeklyRejected || (l.WeeklyUtilization != nil && *l.WeeklyUtilization >= 1)
}

// Mode returns how to treat the account after a successful ping with these limits.
func (l Limits) Mode() Mode {
	switch {
	case l.WeeklyReached():
		return ModeWeeklyReached
	case l.Weekly && !l.FiveHour:
		return ModeNoFiveHour
	default:
		return ModeRolling
	}
}

// ParseLimits reads the all-models five-hour and weekly limits from Claude
// response headers, matching header names in any letter case.
func ParseLimits(headers http.Header) Limits {
	var limits Limits
	for key, values := range headers {
		name, ok := strings.CutPrefix(strings.ToLower(key), limitHeaderPrefix)
		if !ok || len(values) == 0 {
			continue
		}
		value := strings.TrimSpace(values[0])
		if value == "" {
			continue
		}
		switch name {
		case "5h-reset":
			limits.FiveHour = true
			limits.FiveHourReset = parseReset(value)
		case "5h-status", "5h-utilization":
			limits.FiveHour = true
		case "7d-reset":
			limits.Weekly = true
			limits.WeeklyReset = parseReset(value)
		case "7d-status":
			limits.Weekly = true
			limits.WeeklyRejected = strings.EqualFold(value, "rejected")
		case "7d-utilization":
			limits.Weekly = true
			limits.WeeklyUtilization = parseUtilization(value)
		}
	}
	return limits
}

// parseReset reads Unix seconds, or RFC 3339, and returns zero for anything else.
func parseReset(raw string) time.Time {
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		return time.Unix(seconds, 0).UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

func parseUtilization(raw string) *float64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil
	}
	return &value
}

// Outcome is the result of one ping.
type Outcome struct {
	At time.Time
	OK bool
	// Limits are read from a successful reply. Failed replies carry no headers.
	Limits Limits
	// CooldownUntil is the host's retry time for the account after a failure.
	CooldownUntil time.Time
	// Manual marks a ping started from the status page. Scheduled is the next
	// ping that was already planned when it started, and Mode the account's mode then.
	Manual    bool
	Scheduled time.Time
	Mode      Mode
}

// NextPing returns when the account should be pinged again. A failed manual
// ping never delays the ping that was already scheduled. For an account waiting
// for its weekly reset, it does not bring that ping forward either.
func NextPing(o Outcome) time.Time {
	if !o.OK {
		next := o.At.Add(RetryAfterFailure)
		if cooled := o.CooldownUntil.Add(ResetBuffer); !o.CooldownUntil.IsZero() && cooled.After(next) {
			next = cooled
		}
		waiting := o.Mode == ModeNoFiveHour || o.Mode == ModeWeeklyReached
		if o.Manual && !o.Scheduled.IsZero() && (o.Scheduled.Before(next) || waiting) {
			next = o.Scheduled
		}
		return next
	}
	if o.Limits.Mode() == ModeRolling {
		if next, ok := afterReset(o.At, o.Limits.FiveHourReset, fiveHourMaxAhead); ok {
			return next
		}
		return o.At.Add(WindowFallback)
	}
	if next, ok := afterReset(o.At, o.Limits.WeeklyReset, weeklyMaxAhead); ok {
		return next
	}
	return o.At.Add(RecheckFallback)
}

// afterReset returns the ping time for a reported reset, or false when the
// reset is missing or implausible.
func afterReset(at, reset time.Time, maxAhead time.Duration) (time.Time, bool) {
	switch {
	case reset.IsZero():
		return time.Time{}, false
	case reset.After(at):
		if reset.Sub(at) > maxAhead {
			return time.Time{}, false
		}
		return reset.Add(ResetBuffer), true
	case at.Sub(reset) <= SkewTolerance:
		// A reset at or just before the ping time means the local clock runs ahead
		// of Anthropic's and the ping landed in the old window. Retry shortly.
		return at.Add(SkewRetry), true
	default:
		return time.Time{}, false
	}
}
