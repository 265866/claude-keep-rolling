package rolling

import (
	"net/http"
	"path/filepath"
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
		{"success with reset", Outcome{At: at, OK: true, ResetAt: reset}, reset.Add(time.Minute)},
		{"success without reset", Outcome{At: at, OK: true}, at.Add(5*time.Hour + time.Minute)},
		{"reset just passed (fast clock)", Outcome{At: at, OK: true, ResetAt: at.Add(-time.Second)}, at.Add(2 * time.Minute)},
		{"reset exactly now", Outcome{At: at, OK: true, ResetAt: at}, at.Add(2 * time.Minute)},
		{"reset at skew tolerance", Outcome{At: at, OK: true, ResetAt: at.Add(-10 * time.Minute)}, at.Add(2 * time.Minute)},
		{"reset long past", Outcome{At: at, OK: true, ResetAt: at.Add(-11 * time.Minute)}, at.Add(5*time.Hour + time.Minute)},
		{"failure", Outcome{At: at, OK: false, ResetAt: reset}, at.Add(15 * time.Minute)},
		{"failure with short cooldown", Outcome{At: at, OK: false, CooldownUntil: at.Add(5 * time.Minute)}, at.Add(15 * time.Minute)},
		{"failure with window cooldown", Outcome{At: at, OK: false, CooldownUntil: reset}, reset.Add(time.Minute)},
		{"failed manual ping keeps an earlier schedule", Outcome{At: at, OK: false, Manual: true, Scheduled: at.Add(5 * time.Minute)}, at.Add(5 * time.Minute)},
		{"failed manual ping retries before a later schedule", Outcome{At: at, OK: false, Manual: true, Scheduled: reset}, at.Add(15 * time.Minute)},
		{"failed manual ping without a schedule", Outcome{At: at, OK: false, Manual: true}, at.Add(15 * time.Minute)},
		{"failed scheduled ping ignores the old schedule", Outcome{At: at, OK: false, Scheduled: at.Add(5 * time.Minute)}, at.Add(15 * time.Minute)},
		{"successful manual ping follows the new window", Outcome{At: at, OK: true, ResetAt: reset, Manual: true, Scheduled: at.Add(5 * time.Minute)}, reset.Add(time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextPing(tc.outcome); !got.Equal(tc.want) {
				t.Fatalf("NextPing = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestResetTime(t *testing.T) {
	want := time.Unix(1790000000, 0).UTC()
	cases := []struct {
		name    string
		headers http.Header
		ok      bool
	}{
		{"canonical unix", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"1790000000"}}, true},
		{"lowercase key", http.Header{"anthropic-ratelimit-unified-5h-reset": {" 1790000000 "}}, true},
		{"rfc3339", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {want.Format(time.RFC3339)}}, true},
		{"seven day only", http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {"1790000000"}}, false},
		{"garbage", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"soon"}}, false},
		{"zero", http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"0"}}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResetTime(tc.headers)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && !got.Equal(want) {
				t.Fatalf("reset = %s, want %s", got, want)
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
