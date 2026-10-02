package main

import (
	"slices"
	"testing"
	"time"

	"github.com/265866/claude-keep-rolling/internal/rolling"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestClaimDue(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r := newRoller()
	r.states["on-time"] = &accountState{NextPingAt: now}
	r.states["later"] = &accountState{NextPingAt: now.Add(time.Minute)}
	r.states["busy"] = &accountState{Pinging: true}
	r.states["removed"] = &accountState{}
	accounts := []pluginapi.HostAuthFileEntry{
		{ID: "new"},
		{ID: "on-time"},
		{ID: "later"},
		{ID: "busy"},
		{ID: "disabled", Disabled: true},
		{ID: "unselected"},
	}
	settings := rolling.Settings{Accounts: []string{"new", "on-time", "later", "busy", "disabled"}}

	var claimed []string
	for _, account := range r.claimDue(accounts, settings, now) {
		claimed = append(claimed, account.ID)
	}
	if want := []string{"new", "on-time"}; !slices.Equal(claimed, want) {
		t.Fatalf("claimed %v, want %v", claimed, want)
	}
	for _, id := range claimed {
		if !r.states[id].Pinging {
			t.Fatalf("%s should be marked as pinging", id)
		}
	}
	if _, ok := r.states["removed"]; ok {
		t.Fatal("state for a removed account should be dropped")
	}
	if again := r.claimDue(accounts, settings, now); len(again) != 0 {
		t.Fatalf("accounts already pinging were claimed again: %v", again)
	}
}

func TestClaimAllowsOnePingPerAccount(t *testing.T) {
	r := newRoller()
	if !r.claim("a") {
		t.Fatal("first claim should succeed")
	}
	if r.claim("a") {
		t.Fatal("second claim should fail while the first ping runs")
	}
	if !r.claim("b") {
		t.Fatal("another account should not be blocked")
	}
	r.release("a")
	if !r.claim("a") {
		t.Fatal("claim should succeed after release")
	}
}
