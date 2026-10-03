package provider

import (
	"encoding/json"
	"strings"
)

// ClaudeUserOf names the Claude account whose id Claude Code put in a
// request (metadata.user_id's account_uuid): the account Claude Code is
// signed in to, by ~/.claude.json, or a saved one, by the profile it was
// saved with. "" when no account magpie knows has that id.
func ClaudeUserOf(accountUUID string) string {
	if accountUUID == "" {
		return ""
	}
	if acct, ok := claudeProfileAccount(); ok {
		if id, _ := acct["accountUuid"].(string); id == accountUUID {
			if email, _ := acct["emailAddress"].(string); email != "" {
				return email
			}
		}
	}
	for _, l := range readLogins() {
		if l.Agent != "claude" || len(l.Profile) == 0 {
			continue
		}
		var p struct {
			Account string `json:"accountUuid"`
		}
		if json.Unmarshal(l.Profile, &p) == nil && p.Account == accountUUID {
			return l.User
		}
	}
	return ""
}

// ClaudeAccountOf is the account_uuid in a Claude Code request's
// metadata.user_id, which Claude Code sends as a JSON string holding
// device_id, account_uuid and session_id; "" when it has none.
func ClaudeAccountOf(userID string) string {
	userID = strings.TrimSpace(userID)
	if !strings.HasPrefix(userID, "{") {
		return ""
	}
	var id struct {
		Account string `json:"account_uuid"`
	}
	if json.Unmarshal([]byte(userID), &id) != nil {
		return ""
	}
	return id.Account
}
