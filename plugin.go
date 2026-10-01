package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/265866/claude-keep-rolling/internal/rolling"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// pluginVersion is set at build time with -ldflags "-X main.pluginVersion=<version>".
var pluginVersion = "0.0.0-dev"

const (
	pluginID   = "claude-keep-rolling"
	repository = "https://github.com/265866/claude-keep-rolling"

	statePath  = "/v0/management/" + pluginID + "/state"
	pingPath   = "/v0/management/" + pluginID + "/ping"
	statusPath = "/status"
)

//go:embed status.html
var statusPage []byte

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  struct {
		ManagementAPI bool `json:"management_api"`
	} `json:"capabilities"`
}

type managementRegistration struct {
	Routes    []managementRoute    `json:"routes"`
	Resources []managementResource `json:"resources"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method string `json:"Method"`
	Path   string `json:"Path"`
	Body   []byte `json:"Body"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if err := json.Unmarshal(request, &req); err != nil {
				return nil, fmt.Errorf("decode lifecycle request: %w", err)
			}
		}
		keeper.configure(rolling.ParseSettings(req.ConfigYAML))
		keeper.start()
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce:
		keeper.stop()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodPluginShutdown:
		keeper.shutdown()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Routes: []managementRoute{
				{Method: http.MethodGet, Path: statePath, Description: "Claude accounts, settings and ping schedule."},
				{Method: http.MethodPost, Path: pingPath, Description: "Ping one Claude account now."},
			},
			Resources: []managementResource{
				{Path: statusPath, Menu: "Claude Keep Rolling", Description: "Keeps Claude five-hour usage windows rolling."},
			},
		})
	case pluginabi.MethodManagementHandle:
		var req managementRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("decode management request: %w", err)
		}
		return okEnvelope(handleManagement(req))
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	reg := registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Claude Keep Rolling",
			Version:          pluginVersion,
			Author:           "265866",
			GitHubRepository: repository,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "model", Type: pluginapi.ConfigFieldTypeString, Description: "Model used for each ping. Defaults to " + rolling.DefaultModel + "."},
				{Name: "prompt", Type: pluginapi.ConfigFieldTypeString, Description: "Message sent with each ping. Defaults to hi."},
				{Name: "select_all", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Keep every Claude account rolling, including accounts added later. Defaults to true."},
				{Name: "accounts", Type: pluginapi.ConfigFieldTypeArray, Description: "Account IDs to keep rolling when select_all is false."},
			},
		},
	}
	reg.Capabilities.ManagementAPI = true
	return reg
}

func handleManagement(req managementRequest) managementResponse {
	switch {
	case req.Method == http.MethodGet && req.Path == "/v0/resource/plugins/"+pluginID+statusPath:
		return managementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}},
			Body:       statusPage,
		}
	case req.Method == http.MethodGet && req.Path == statePath:
		return jsonResponse(http.StatusOK, buildState(time.Now()))
	case req.Method == http.MethodPost && req.Path == pingPath:
		return handlePing(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

type accountView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Disabled   bool       `json:"disabled"`
	Selected   bool       `json:"selected"`
	Pinging    bool       `json:"pinging"`
	LastPingAt *time.Time `json:"last_ping_at,omitempty"`
	LastOK     bool       `json:"last_ok"`
	LastStatus int        `json:"last_status,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	ResetAt    *time.Time `json:"reset_at,omitempty"`
	NextPingAt *time.Time `json:"next_ping_at,omitempty"`
}

type stateView struct {
	Version     string           `json:"version"`
	Now         time.Time        `json:"now"`
	Settings    rolling.Settings `json:"settings"`
	ConfigError string           `json:"config_error,omitempty"`
	Accounts    []accountView    `json:"accounts"`
	ListError   string           `json:"list_error,omitempty"`
}

func buildState(now time.Time) stateView {
	settings, configError := keeper.currentSettings()
	view := stateView{Version: pluginVersion, Now: now.UTC(), Settings: settings, ConfigError: configError, Accounts: []accountView{}}
	accounts, err := listClaudeAccounts()
	if err != nil {
		view.ListError = err.Error()
		return view
	}
	for _, account := range accounts {
		item := accountView{
			ID:       account.ID,
			Name:     accountName(account),
			Disabled: account.Disabled,
			Selected: settings.Selected(account.ID),
		}
		if state, ok := keeper.snapshot(account.ID); ok {
			item.Pinging = state.Pinging
			item.LastPingAt = timeOrNil(state.LastPingAt)
			item.LastOK = state.LastOK
			item.LastStatus = state.LastStatus
			item.LastError = state.LastError
			item.ResetAt = timeOrNil(state.ResetAt)
			item.NextPingAt = timeOrNil(state.NextPingAt)
		}
		view.Accounts = append(view.Accounts, item)
	}
	return view
}

func handlePing(req managementRequest) managementResponse {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || body.ID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "body must be {\"id\": \"<account id>\"}"})
	}
	accounts, err := listClaudeAccounts()
	if err != nil {
		return jsonResponse(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	for _, account := range accounts {
		if account.ID != body.ID {
			continue
		}
		if account.Disabled {
			return jsonResponse(http.StatusConflict, map[string]string{"error": "account is disabled"})
		}
		if !keeper.claim(account.ID) {
			return jsonResponse(http.StatusConflict, map[string]string{"error": "a ping is already running for this account"})
		}
		settings, _ := keeper.currentSettings()
		keeper.ping(account, settings, true)
		return jsonResponse(http.StatusOK, buildState(time.Now()))
	}
	return jsonResponse(http.StatusNotFound, map[string]string{"error": "Claude account not found"})
}

func jsonResponse(status int, v any) managementResponse {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"encode response"}`)
	}
	return managementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}},
		Body:       body,
	}
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}
