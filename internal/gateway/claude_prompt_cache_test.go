package gateway

import (
	"slices"
	"strings"
	"testing"
)

// The caller's instructions are a block of their own, marked for an hour's
// cache, ahead of the messages: two requests with the same instructions
// and different messages share them in the cache. Claude Code writes an
// hour's cache too, as a 1h mark may not follow a 5m one.
func TestClaudeInstructionsCachedApart(t *testing.T) {
	for _, said := range []string{"one", "two"} {
		blocks, err := renderClaudePrompt(&Request{System: "rules", Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: said}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(blocks) != 2 || blocks[0]["text"] != "<external_system_instructions>\nrules\n</external_system_instructions>\n\n" {
			t.Fatalf("blocks: %v", blocks)
		}
		if cc, _ := blocks[0]["cache_control"].(map[string]string); cc["ttl"] != "1h" || blocks[1]["cache_control"] != nil {
			t.Fatalf("cache marks: %v", blocks)
		}
		if !strings.Contains(blocks[1]["text"].(string), "Human: "+said) {
			t.Fatalf("messages: %v", blocks[1])
		}
	}
	blocks, _ := renderClaudePrompt(&Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}})
	if len(blocks) != 1 || blocks[0]["cache_control"] != nil {
		t.Fatalf("no instructions: %v", blocks)
	}
	env := cleanClaudeEnv([]string{"CLAUDE_CODE_PROMPT_CACHE_TTL=5m"})
	if !slices.Contains(env, "CLAUDE_CODE_PROMPT_CACHE_TTL=1h") || slices.Contains(env, "CLAUDE_CODE_PROMPT_CACHE_TTL=5m") {
		t.Fatalf("env: %q", env)
	}
}

// Claude Code as the caller begins its system prompt with a billing header
// whose version ends in a part made from the user's message: left in, the
// instructions differed with each message and were never read from the
// cache. They are the same for two messages, without the header.
func TestClaudeInstructionsWithoutBillingHeader(t *testing.T) {
	var first any
	for _, v := range []string{"5ea", "820"} {
		req, err := parseAnthropic([]byte(`{"model":"m","max_tokens":10,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.288.` + v + `; cc_entrypoint=sdk-cli;"},{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Rules.\\n","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"comment ` + v + `"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		blocks, _ := renderClaudePrompt(req)
		text := blocks[0]["text"].(string)
		if strings.Contains(text, "billing-header") || !strings.HasPrefix(text, "<external_system_instructions>\nYou are a Claude agent") {
			t.Fatalf("instructions: %q", text)
		}
		if first == nil {
			first = text
		} else if text != first {
			t.Fatalf("instructions differ: %q / %q", first, text)
		}
	}
}
