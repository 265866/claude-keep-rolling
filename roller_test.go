package main

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestTruncate(t *testing.T) {
	cases := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{"short", "  rate limited  ", 20, "rate limited"},
		{"exact", "abcde", 5, "abcde"},
		{"ascii", "abcdef", 5, "abcde..."},
		// "é" is two bytes, so a 5-byte cut lands inside the third one.
		{"multibyte", "ééé", 5, "éé..."},
		{"emoji", "ok 👍👍", 5, "ok ..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.value, tc.limit)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.value, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) = %q is not valid UTF-8", tc.value, tc.limit, got)
			}
			if len(strings.TrimSuffix(got, "...")) > tc.limit {
				t.Fatalf("truncate(%q, %d) = %q is over the limit", tc.value, tc.limit, got)
			}
		})
	}
}
