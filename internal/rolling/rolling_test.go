package rolling

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestParseSettingsDefaults(t *testing.T) {
	settings, err := ParseSettings([]byte("enabled: true\npriority: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "claude-haiku-4-5-20251001" || settings.Prompt != "hi" || !settings.SelectAll || len(settings.Accounts) != 0 {
		t.Fatalf("unexpected defaults: %+v", settings)
	}
}

func TestParseSettingsExplicit(t *testing.T) {
	raw := []byte(`
enabled: true
store:
  id: claude-keep-rolling
model: claude-sonnet-4-6
prompt: "  hello there "
select_all: false
accounts:
  - claude-a@example.com.json
  - claude-b@example.com.json
`)
	settings, err := ParseSettings(raw)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q", settings.Model)
	}
	if settings.Prompt != "  hello there " {
		t.Fatalf("prompt should be sent as typed, got %q", settings.Prompt)
	}
	if settings.SelectAll {
		t.Fatal("select_all should be false")
	}
	if !settings.Selected("claude-b@example.com.json") || settings.Selected("claude-c@example.com.json") {
		t.Fatalf("selection mismatch: %+v", settings.Accounts)
	}
}

func TestParseSettingsBlankValuesFallBack(t *testing.T) {
	settings, err := ParseSettings([]byte("model: \"  \"\nprompt: \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != DefaultModel || settings.Prompt != DefaultPrompt {
		t.Fatalf("blank values should fall back: %+v", settings)
	}
}

func TestParseSettingsRejectsInvalidYAML(t *testing.T) {
	if _, err := ParseSettings([]byte("accounts: [unclosed")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSelectAllIncludesUnknownAccounts(t *testing.T) {
	settings := Settings{SelectAll: true}
	if !settings.Selected("claude-added-later.json") {
		t.Fatal("select all must include accounts added later")
	}
}

func TestNextPing(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	reset := at.Add(4*time.Hour + 30*time.Minute)
	cases := []struct {
		name    string
		outcome Outcome
		want    time.Time
	}{
		{"success with reset", Outcome{At: at, OK: true, Limits: fiveHour(reset)}, reset.Add(time.Minute)},
		{"success without reset", Outcome{At: at, OK: true}, at.Add(5*time.Hour + time.Minute)},
		{"reset just passed (fast clock)", Outcome{At: at, OK: true, Limits: fiveHour(at.Add(-time.Second))}, at.Add(2 * time.Minute)},
		{"reset exactly now", Outcome{At: at, OK: true, Limits: fiveHour(at)}, at.Add(2 * time.Minute)},
		{"reset at skew tolerance", Outcome{At: at, OK: true, Limits: fiveHour(at.Add(-10 * time.Minute))}, at.Add(2 * time.Minute)},
		{"reset long past", Outcome{At: at, OK: true, Limits: fiveHour(at.Add(-11 * time.Minute))}, at.Add(5*time.Hour + time.Minute)},
		{"failure", Outcome{At: at, OK: false, Limits: fiveHour(reset)}, at.Add(15 * time.Minute)},
		{"failure with short cooldown", Outcome{At: at, OK: false, CooldownUntil: at.Add(5 * time.Minute)}, at.Add(15 * time.Minute)},
		{"failure with window cooldown", Outcome{At: at, OK: false, CooldownUntil: reset}, reset.Add(time.Minute)},
		{"failed manual ping keeps an earlier schedule", Outcome{At: at, OK: false, Manual: true, Scheduled: at.Add(5 * time.Minute)}, at.Add(5 * time.Minute)},
		{"failed manual ping retries before a later schedule", Outcome{At: at, OK: false, Manual: true, Scheduled: reset}, at.Add(15 * time.Minute)},
		{"failed manual ping without a schedule", Outcome{At: at, OK: false, Manual: true}, at.Add(15 * time.Minute)},
		{"failed scheduled ping ignores the old schedule", Outcome{At: at, OK: false, Scheduled: at.Add(5 * time.Minute)}, at.Add(15 * time.Minute)},
		{"successful manual ping follows the new window", Outcome{At: at, OK: true, Limits: fiveHour(reset), Manual: true, Scheduled: at.Add(5 * time.Minute)}, reset.Add(time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextPing(tc.outcome); !got.Equal(tc.want) {
				t.Fatalf("NextPing = %s, want %s", got, tc.want)
			}
		})
	}
}

func fiveHour(reset time.Time) Limits {
	return Limits{FiveHour: true, FiveHourReset: reset}
}

func unix(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

// TestScheduleFromHeaders feeds reply headers through ParseLimits and NextPing.
// The "live" cases copy the headers CLIProxyAPI stored for two accounts on 3 Nov 2026.
func TestScheduleFromHeaders(t *testing.T) {
	at := time.Date(2026, 11, 3, 2, 39, 0, 0, time.UTC)
	fiveReset := time.Unix(1793689200, 0).UTC() // 3 Nov 07:00
	weekReset := time.Unix(1793880000, 0).UTC() // 5 Nov 12:00
	cases := []struct {
		name     string
		headers  http.Header
		wantMode Mode
		want     time.Time
	}{
		// Rolling.
		{"rolling", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":       {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Reset":       {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.42"},
		}, ModeRolling, fiveReset.Add(time.Minute)},
		{"rolling with a weekly warning", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":       {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-7d-Status":      {"allowed_warning"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.99"},
		}, ModeRolling, fiveReset.Add(time.Minute)},
		{"rolling without weekly headers", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset": {unix(fiveReset)},
		}, ModeRolling, fiveReset.Add(time.Minute)},
		{"no headers", nil, ModeRolling, at.Add(5*time.Hour + time.Minute)},
		{"garbled five-hour reset", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":  {"soon"},
			"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Reset":  {unix(weekReset)},
		}, ModeRolling, at.Add(5*time.Hour + time.Minute)},
		{"five-hour reset at the limit ahead", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset": {unix(at.Add(6 * time.Hour))},
		}, ModeRolling, at.Add(6*time.Hour + time.Minute)},
		{"five-hour reset too far ahead", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset": {unix(at.Add(6*time.Hour + time.Second))},
		}, ModeRolling, at.Add(5*time.Hour + time.Minute)},
		{"garbled weekly utilization", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":       {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-7d-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"full"},
		}, ModeRolling, fiveReset.Add(time.Minute)},
		{"model-scoped weekly limit reached", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":          {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-7d-Reset":          {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d-Status":         {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Utilization":    {"0.40"},
			"Anthropic-Ratelimit-Unified-7d_oi-Reset":       {unix(at.Add(time.Hour))},
			"Anthropic-Ratelimit-Unified-7d_oi-Status":      {"rejected"},
			"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"1.02"},
			"Anthropic-Ratelimit-Unified-Status":            {"rejected"},
		}, ModeRolling, fiveReset.Add(time.Minute)},
		{"only model-scoped headers", http.Header{
			"Anthropic-Ratelimit-Unified-7d_oi-Reset":  {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d_oi-Status": {"rejected"},
		}, ModeRolling, at.Add(5*time.Hour + time.Minute)},

		// No five-hour limit.
		{"weekly only (live f2a3f5069de30215)", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset":                {"1794272400"},
			"Anthropic-Ratelimit-Unified-7d-Status":               {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Utilization":          {"0.01"},
			"Anthropic-Ratelimit-Unified-Fallback-Percentage":     {"0.5"},
			"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": {"org_level_disabled"},
			"Anthropic-Ratelimit-Unified-Overage-Status":          {"rejected"},
			"Anthropic-Ratelimit-Unified-Representative-Claim":    {"seven_day"},
			"Anthropic-Ratelimit-Unified-Reset":                   {"1794272400"},
			"Anthropic-Ratelimit-Unified-Status":                  {"allowed"},
		}, ModeNoFiveHour, time.Unix(1794272400, 0).UTC().Add(time.Minute)},
		{"weekly only without a weekly reset", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.01"},
		}, ModeNoFiveHour, at.Add(24 * time.Hour)},
		{"weekly only with a garbled weekly reset", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset":  {"next tuesday"},
			"Anthropic-Ratelimit-Unified-7d-Status": {"allowed"},
		}, ModeNoFiveHour, at.Add(24 * time.Hour)},
		{"weekly only with a reset at the limit ahead", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset": {unix(at.Add(7*24*time.Hour + time.Hour))},
		}, ModeNoFiveHour, at.Add(7*24*time.Hour + time.Hour + time.Minute)},
		{"weekly only with a reset too far ahead", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset": {unix(at.Add(7*24*time.Hour + time.Hour + time.Second))},
		}, ModeNoFiveHour, at.Add(24 * time.Hour)},
		{"weekly only with blank five-hour headers", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset": {"  "},
			"Anthropic-Ratelimit-Unified-7d-Reset": {unix(weekReset)},
		}, ModeNoFiveHour, weekReset.Add(time.Minute)},

		// Weekly limit reached.
		{"weekly rejected while five-hour allowed (live 8dc6f68dec36dcc4)", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":                {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-5h-Status":               {"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization":          {"0.01"},
			"Anthropic-Ratelimit-Unified-7d-Reset":                {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d-Status":               {"rejected"},
			"Anthropic-Ratelimit-Unified-7d-Surpassed-Threshold":  {"1.0"},
			"Anthropic-Ratelimit-Unified-7d-Utilization":          {"1.0"},
			"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": {"org_level_disabled"},
			"Anthropic-Ratelimit-Unified-Representative-Claim":    {"seven_day"},
			"Anthropic-Ratelimit-Unified-Reset":                   {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-Status":                  {"rejected"},
		}, ModeWeeklyReached, weekReset.Add(time.Minute)},
		{"weekly rejected, utilization missing", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":  {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Reset":  {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d-Status": {"Rejected"},
		}, ModeWeeklyReached, weekReset.Add(time.Minute)},
		{"weekly utilization at 1 while status allowed", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":       {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-7d-Reset":       {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d-Status":      {"allowed_warning"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"1"},
		}, ModeWeeklyReached, weekReset.Add(time.Minute)},
		{"weekly utilization over 1", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset":       {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"1.02"},
		}, ModeWeeklyReached, weekReset.Add(time.Minute)},
		{"weekly reached without a weekly reset", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":  {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"},
		}, ModeWeeklyReached, at.Add(24 * time.Hour)},

		// Clock skew on the weekly reset, as for the five-hour reset.
		{"weekly reset just passed (fast clock)", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset":  {unix(at.Add(-30 * time.Second))},
			"Anthropic-Ratelimit-Unified-7d-Status": {"allowed"},
		}, ModeNoFiveHour, at.Add(2 * time.Minute)},
		{"weekly reset exactly now while rejected", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":  {unix(fiveReset)},
			"Anthropic-Ratelimit-Unified-7d-Reset":  {unix(at)},
			"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"},
		}, ModeWeeklyReached, at.Add(2 * time.Minute)},
		{"weekly reset at skew tolerance", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset":  {unix(at.Add(-10 * time.Minute))},
			"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"},
		}, ModeWeeklyReached, at.Add(2 * time.Minute)},
		{"weekly reset long past", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset":  {unix(at.Add(-11 * time.Minute))},
			"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"},
		}, ModeWeeklyReached, at.Add(24 * time.Hour)},
		{"five-hour reset just passed with weekly headers", http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset":  {unix(at.Add(-time.Minute))},
			"Anthropic-Ratelimit-Unified-7d-Reset":  {unix(weekReset)},
			"Anthropic-Ratelimit-Unified-7d-Status": {"allowed"},
		}, ModeRolling, at.Add(2 * time.Minute)},

		// Header names in any letter case.
		{"lowercase names", http.Header{
			"anthropic-ratelimit-unified-7d-reset":  {unix(weekReset)},
			"anthropic-ratelimit-unified-7d-status": {"rejected"},
		}, ModeWeeklyReached, weekReset.Add(time.Minute)},
		{"uppercase names", http.Header{
			"ANTHROPIC-RATELIMIT-UNIFIED-7D-RESET":       {unix(weekReset)},
			"ANTHROPIC-RATELIMIT-UNIFIED-7D-UTILIZATION": {"0.3"},
		}, ModeNoFiveHour, weekReset.Add(time.Minute)},
		{"mixed-case names", http.Header{
			"Anthropic-RateLimit-Unified-5H-Reset": {unix(fiveReset)},
			"anthropic-Ratelimit-unified-7d-Reset": {unix(weekReset)},
		}, ModeRolling, fiveReset.Add(time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limits := ParseLimits(tc.headers)
			if mode := limits.Mode(); mode != tc.wantMode {
				t.Fatalf("Mode = %q, want %q (limits %+v)", mode, tc.wantMode, limits)
			}
			if got := NextPing(Outcome{At: at, OK: true, Limits: limits}); !got.Equal(tc.want) {
				t.Fatalf("NextPing = %s, want %s", got, tc.want)
			}
		})
	}
}

// A failed reply carries no headers, so the failure path decides the next ping.
// A failed manual ping leaves an account that waits for its weekly reset alone.
func TestNextPingAfterFailureByMode(t *testing.T) {
	at := time.Date(2026, 11, 3, 2, 39, 0, 0, time.UTC)
	scheduled := time.Date(2026, 11, 7, 1, 1, 0, 0, time.UTC)
	cooldown := time.Date(2026, 11, 5, 12, 0, 10, 0, time.UTC)
	cases := []struct {
		name    string
		outcome Outcome
		want    time.Time
	}{
		{"weekly re-check fails", Outcome{At: at, Mode: ModeNoFiveHour, Scheduled: at}, at.Add(15 * time.Minute)},
		{"weekly-reached ping fails with a weekly cooldown", Outcome{At: at, Mode: ModeWeeklyReached, CooldownUntil: cooldown, Scheduled: at}, cooldown.Add(time.Minute)},
		{"failed manual ping keeps the weekly re-check", Outcome{At: at, Mode: ModeNoFiveHour, Manual: true, Scheduled: scheduled}, scheduled},
		{"failed manual ping keeps the weekly resume", Outcome{At: at, Mode: ModeWeeklyReached, Manual: true, Scheduled: scheduled}, scheduled},
		{"failed manual ping keeps the weekly resume over a cooldown", Outcome{At: at, Mode: ModeWeeklyReached, Manual: true, Scheduled: scheduled, CooldownUntil: cooldown}, scheduled},
		{"failed manual ping without a schedule", Outcome{At: at, Mode: ModeWeeklyReached, Manual: true}, at.Add(15 * time.Minute)},
		{"failed manual ping on a rolling account retries", Outcome{At: at, Mode: ModeRolling, Manual: true, Scheduled: scheduled}, at.Add(15 * time.Minute)},
		{"failed manual ping before any success retries", Outcome{At: at, Manual: true, Scheduled: scheduled}, at.Add(15 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextPing(tc.outcome); !got.Equal(tc.want) {
				t.Fatalf("NextPing = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	reset := time.Unix(1790000000, 0).UTC()
	util := func(v float64) *float64 { return &v }
	cases := []struct {
		name    string
		headers http.Header
		want    Limits
	}{
		{"canonical unix", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"1790000000"}}, fiveHour(reset)},
		{"lowercase key and padded value", http.Header{"anthropic-ratelimit-unified-5h-reset": {" 1790000000 "}}, fiveHour(reset)},
		{"rfc3339", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {reset.Format(time.RFC3339)}}, fiveHour(reset)},
		{"garbage reset", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"soon"}}, Limits{FiveHour: true}},
		{"zero reset", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"0"}}, Limits{FiveHour: true}},
		{"negative reset", http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {"-5"}}, Limits{Weekly: true}},
		{"first of repeated values", http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {"1790000000", "1"}}, Limits{Weekly: true, WeeklyReset: reset}},
		{"empty value list", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {}}, Limits{}},
		{"weekly fields", http.Header{
			"Anthropic-Ratelimit-Unified-7d-Reset":       {"1790000000"},
			"Anthropic-Ratelimit-Unified-7d-Status":      {"REJECTED"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.75"},
		}, Limits{Weekly: true, WeeklyReset: reset, WeeklyRejected: true, WeeklyUtilization: util(0.75)}},
		{"garbled utilization", http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"75%"}}, Limits{Weekly: true}},
		{"negative utilization", http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"-0.1"}}, Limits{Weekly: true}},
		{"NaN utilization", http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"NaN"}}, Limits{Weekly: true}},
		{"infinite utilization", http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"+Inf"}}, Limits{Weekly: true}},
		{"unknown status", http.Header{"Anthropic-Ratelimit-Unified-7d-Status": {"invalid"}}, Limits{Weekly: true}},
		{"model-scoped and overall headers ignored", http.Header{
			"Anthropic-Ratelimit-Unified-7d_oi-Status":      {"rejected"},
			"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"1.02"},
			"Anthropic-Ratelimit-Unified-Status":            {"rejected"},
			"Anthropic-Ratelimit-Unified-Reset":             {"1790000000"},
			"Retry-After":                                   {"60"},
		}, Limits{}},
		{"nil", nil, Limits{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseLimits(tc.headers)
			if got.FiveHour != tc.want.FiveHour || got.Weekly != tc.want.Weekly ||
				!got.FiveHourReset.Equal(tc.want.FiveHourReset) || !got.WeeklyReset.Equal(tc.want.WeeklyReset) ||
				got.WeeklyRejected != tc.want.WeeklyRejected {
				t.Fatalf("ParseLimits = %+v, want %+v", got, tc.want)
			}
			switch {
			case (got.WeeklyUtilization == nil) != (tc.want.WeeklyUtilization == nil):
				t.Fatalf("WeeklyUtilization = %v, want %v", got.WeeklyUtilization, tc.want.WeeklyUtilization)
			case got.WeeklyUtilization != nil && *got.WeeklyUtilization != *tc.want.WeeklyUtilization:
				t.Fatalf("WeeklyUtilization = %v, want %v", *got.WeeklyUtilization, *tc.want.WeeklyUtilization)
			}
		})
	}
}

func TestHostConfigPath(t *testing.T) {
	wd := filepath.Join("srv", "cpa")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"separate value", []string{"/CLIProxyAPI/CLIProxyAPI", "-config", "/data/config.yaml", "-local-model"}, "/data/config.yaml"},
		{"equals form", []string{"cpa", "--config=/etc/cpa.yaml"}, "/etc/cpa.yaml"},
		{"double dash separate", []string{"cpa", "--config", "c.yaml"}, "c.yaml"},
		{"no flag", []string{"cpa", "-local-model"}, filepath.Join(wd, "config.yaml")},
		{"flag without value", []string{"cpa", "-config"}, filepath.Join(wd, "config.yaml")},
		{"empty equals", []string{"cpa", "-config="}, filepath.Join(wd, "config.yaml")},
		{"similar flag", []string{"cpa", "-config-dir", "x"}, filepath.Join(wd, "config.yaml")},
		{"value named config", []string{"cpa", "config"}, filepath.Join(wd, "config.yaml")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostConfigPath(tc.args, wd); got != tc.want {
				t.Fatalf("HostConfigPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPluginEnabled(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"enabled", "plugins:\n  enabled: true\n  configs:\n    claude-keep-rolling:\n      enabled: true\n", true},
		{"plugin disabled", "plugins:\n  enabled: true\n  configs:\n    claude-keep-rolling:\n      enabled: false\n", false},
		{"plugins disabled", "plugins:\n  enabled: false\n  configs:\n    claude-keep-rolling:\n      enabled: true\n", false},
		{"not configured", "plugins:\n  enabled: true\n  configs:\n    other:\n      enabled: true\n", false},
		{"no plugins section", "port: 8317\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PluginEnabled([]byte(tc.raw), "claude-keep-rolling")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("PluginEnabled = %v, want %v", got, tc.want)
			}
		})
	}
	if _, err := PluginEnabled([]byte("plugins: [unclosed"), "claude-keep-rolling"); err == nil {
		t.Fatal("expected a parse error")
	}
}
