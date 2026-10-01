package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/265866/claude-keep-rolling/internal/rolling"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	// startupDelay gives the host time to finish loading auths and executors.
	startupDelay = 20 * time.Second
	tickInterval = 30 * time.Second
	// shutdownWait bounds how long shutdown waits for in-flight pings.
	shutdownWait = time.Minute
	// pingMaxTokens keeps the reply tiny; it is not user-configurable.
	pingMaxTokens = 1
)

var keeper = newRoller()

type accountState struct {
	LastPingAt time.Time
	LastOK     bool
	LastStatus int
	LastError  string
	ResetAt    time.Time
	NextPingAt time.Time
	Pinging    bool
}

type roller struct {
	mu          sync.Mutex
	settings    rolling.Settings
	configError string
	states      map[string]*accountState
	stopCh      chan struct{}
	// background tracks the loop and its pings so shutdown can wait for host calls to finish.
	background sync.WaitGroup
	// configWarned records that an unreadable host config was already logged.
	configWarned bool
}

func newRoller() *roller {
	settings, _ := rolling.ParseSettings(nil)
	return &roller{settings: settings, states: map[string]*accountState{}}
}

// configure applies new settings. Invalid settings keep the previous ones and are reported on the status page.
func (r *roller) configure(settings rolling.Settings, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.configError = err.Error()
		return
	}
	r.settings = settings
	r.configError = ""
}

func (r *roller) currentSettings() (rolling.Settings, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settings, r.configError
}

func (r *roller) start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopCh != nil {
		return
	}
	stop := make(chan struct{})
	r.stopCh = stop
	r.background.Add(1)
	go func() {
		defer r.background.Done()
		r.run(stop)
	}()
}

func (r *roller) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopCh != nil {
		close(r.stopCh)
		r.stopCh = nil
	}
}

// shutdown stops the loop and waits, up to shutdownWait, for background host calls to return.
func (r *roller) shutdown() {
	r.stop()
	done := make(chan struct{})
	go func() {
		r.background.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
	}
}

func (r *roller) run(stop <-chan struct{}) {
	timer := time.NewTimer(startupDelay)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		r.tick(stop, time.Now())
		timer.Reset(tickInterval)
	}
}

// tick starts a ping for every selected, enabled Claude account whose next ping is due.
// Pings run concurrently so one slow account does not hold up the others.
func (r *roller) tick(stop <-chan struct{}, now time.Time) {
	if !r.hostEnablesPlugin() {
		return
	}
	accounts, err := listClaudeAccounts()
	if err != nil {
		hostLog("warn", "listing Claude accounts failed", map[string]any{"error": err.Error()})
		return
	}
	settings, _ := r.currentSettings()
	for _, account := range r.claimDue(accounts, settings, now) {
		select {
		case <-stop:
			r.release(account.ID)
			continue
		default:
		}
		r.background.Add(1)
		go func() {
			defer r.background.Done()
			r.ping(account, settings, false)
		}()
	}
}

// hostEnablesPlugin reads the host config file. If the file cannot be read, as with
// database-backed configs, it assumes the plugin is enabled and logs that once.
func (r *roller) hostEnablesPlugin() bool {
	workDir, _ := os.Getwd()
	path := rolling.HostConfigPath(os.Args, workDir)
	raw, err := os.ReadFile(path)
	var enabled bool
	if err == nil {
		enabled, err = rolling.PluginEnabled(raw, pluginID)
	}
	if err != nil {
		r.mu.Lock()
		warned := r.configWarned
		r.configWarned = true
		r.mu.Unlock()
		if !warned {
			hostLog("warn", "cannot read the host config to check whether the plugin is disabled; pings continue until the host stops the plugin", map[string]any{"path": path, "error": err.Error()})
		}
		return true
	}
	return enabled
}

// claimDue drops state for accounts that no longer exist and marks due accounts as pinging.
func (r *roller) claimDue(accounts []pluginapi.HostAuthFileEntry, settings rolling.Settings, now time.Time) []pluginapi.HostAuthFileEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	present := make(map[string]bool, len(accounts))
	var due []pluginapi.HostAuthFileEntry
	for _, account := range accounts {
		present[account.ID] = true
		if account.Disabled || !settings.Selected(account.ID) {
			continue
		}
		state := r.stateLocked(account.ID)
		if state.Pinging || now.Before(state.NextPingAt) {
			continue
		}
		state.Pinging = true
		due = append(due, account)
	}
	for id := range r.states {
		if !present[id] {
			delete(r.states, id)
		}
	}
	return due
}

// claim marks one account as pinging for a manual ping. It fails if a ping is already running.
func (r *roller) claim(accountID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.stateLocked(accountID)
	if state.Pinging {
		return false
	}
	state.Pinging = true
	return true
}

func (r *roller) release(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stateLocked(accountID).Pinging = false
}

func (r *roller) snapshot(accountID string) (accountState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[accountID]
	if !ok {
		return accountState{}, false
	}
	return *state, true
}

func (r *roller) stateLocked(accountID string) *accountState {
	state, ok := r.states[accountID]
	if !ok {
		state = &accountState{}
		r.states[accountID] = state
	}
	return state
}

// ping sends one message through the exact account and records when to ping it next.
// The caller must have claimed the account. A failed manual ping never delays a scheduled one.
func (r *roller) ping(account pluginapi.HostAuthFileEntry, settings rolling.Settings, manual bool) {
	at := time.Now()
	resetAt, status, err := sendPing(account.ID, settings)
	outcome := rolling.Outcome{At: at, OK: err == nil, ResetAt: resetAt}
	if err != nil {
		outcome.CooldownUntil = cooldownUntil(account.ID)
	}
	next := rolling.NextPing(outcome)

	r.mu.Lock()
	state := r.stateLocked(account.ID)
	if err != nil && manual && !state.NextPingAt.IsZero() && state.NextPingAt.Before(next) {
		next = state.NextPingAt
	}
	state.Pinging = false
	state.LastPingAt = at
	state.LastOK = err == nil
	state.LastStatus = status
	state.LastError = ""
	if err != nil {
		state.LastError = err.Error()
	} else {
		state.ResetAt = resetAt
	}
	state.NextPingAt = next
	r.mu.Unlock()

	fields := map[string]any{"account": accountName(account), "next_ping": next.Format(time.RFC3339)}
	if err != nil {
		fields["error"] = err.Error()
		hostLog("warn", "Claude keep-rolling ping failed", fields)
		return
	}
	if !resetAt.IsZero() {
		fields["window_reset"] = resetAt.Format(time.RFC3339)
	}
	hostLog("info", "Claude keep-rolling ping sent", fields)
}

// cooldownUntil returns the retry time CPA recorded for the account after a failure,
// which for a rate limit is the window reset parsed from the rejected response.
func cooldownUntil(accountID string) time.Time {
	accounts, err := listClaudeAccounts()
	if err != nil {
		return time.Time{}
	}
	for _, account := range accounts {
		if account.ID == accountID {
			return account.NextRetryAfter
		}
	}
	return time.Time{}
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []claudeMessage `json:"messages"`
}

// sendPing returns the window reset time and HTTP status. No tools are declared,
// so the model can only answer with text. No host callback ID is passed, so a
// manual ping is not cancelled when its browser request goes away.
func sendPing(accountID string, settings rolling.Settings) (time.Time, int, error) {
	body, err := json.Marshal(claudeRequest{
		Model:     settings.Model,
		MaxTokens: pingMaxTokens,
		Messages:  []claudeMessage{{Role: "user", Content: settings.Prompt}},
	})
	if err != nil {
		return time.Time{}, 0, err
	}
	raw, err := callHost(pluginabi.MethodHostModelExecute, pluginapi.HostModelExecutionRequest{
		EntryProtocol:  "claude",
		ExitProtocol:   "claude",
		Model:          settings.Model,
		Body:           body,
		ForcedProvider: "claude",
		AuthID:         accountID,
	})
	if err != nil {
		var hostErr *hostError
		if errors.As(err, &hostErr) {
			return time.Time{}, hostErr.Status, err
		}
		return time.Time{}, 0, err
	}
	var resp pluginapi.HostModelExecutionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return time.Time{}, 0, fmt.Errorf("decode model response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return time.Time{}, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(resp.Body), 300))
	}
	resetAt, _ := rolling.ResetTime(resp.Headers)
	return resetAt, resp.StatusCode, nil
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

// listClaudeAccounts returns Claude OAuth accounts sorted by display name.
func listClaudeAccounts() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var list authListResponse
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode auth list: %w", err)
	}
	accounts := make([]pluginapi.HostAuthFileEntry, 0, len(list.Files))
	for _, file := range list.Files {
		if strings.TrimSpace(file.ID) == "" || !isClaudeOAuth(file) {
			continue
		}
		accounts = append(accounts, file)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return strings.ToLower(accountName(accounts[i])) < strings.ToLower(accountName(accounts[j]))
	})
	return accounts, nil
}

// isClaudeOAuth excludes Claude API-key credentials, which bill per request and have no usage window.
func isClaudeOAuth(file pluginapi.HostAuthFileEntry) bool {
	claude := strings.EqualFold(strings.TrimSpace(file.Provider), "claude") || strings.EqualFold(strings.TrimSpace(file.Type), "claude")
	accountType := strings.TrimSpace(file.AccountType)
	return claude && (accountType == "" || strings.EqualFold(accountType, "oauth"))
}

func accountName(account pluginapi.HostAuthFileEntry) string {
	for _, name := range []string{account.Email, account.Label, account.Name, account.ID} {
		if strings.TrimSpace(name) != "" {
			return name
		}
	}
	return ""
}

func hostLog(level, message string, fields map[string]any) {
	fields["plugin"] = pluginID
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{"level": level, "message": message, "fields": fields})
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
