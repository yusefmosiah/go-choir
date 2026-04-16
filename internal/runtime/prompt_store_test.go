package runtime

import (
	"testing"
)

func TestPromptStoreSeedsDefaults(t *testing.T) {
	store := NewPromptStore(t.TempDir())

	prompts, err := store.List("user-alice")
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}

	if len(prompts) != len(promptRoles()) {
		t.Fatalf("prompt count = %d, want %d", len(prompts), len(promptRoles()))
	}

	for _, prompt := range prompts {
		if prompt.Role == "" {
			t.Fatal("prompt role should not be empty")
		}
		if prompt.Content == "" {
			t.Fatalf("prompt %s content should not be empty", prompt.Role)
		}
		if prompt.Source != "default" {
			t.Fatalf("prompt %s source = %q, want default", prompt.Role, prompt.Source)
		}
	}
}

func TestPromptStoreSupportsUserOverridesAndReset(t *testing.T) {
	store := NewPromptStore(t.TempDir())

	saved, err := store.Save("user-alice", AgentProfileVText, "Custom vtext prompt")
	if err != nil {
		t.Fatalf("save prompt override: %v", err)
	}
	if saved.Source != "user" {
		t.Fatalf("saved source = %q, want user", saved.Source)
	}

	loaded, err := store.Load("user-alice", AgentProfileVText)
	if err != nil {
		t.Fatalf("load prompt override: %v", err)
	}
	if loaded.Content != "Custom vtext prompt" {
		t.Fatalf("loaded content = %q, want custom override", loaded.Content)
	}
	if loaded.Source != "user" {
		t.Fatalf("loaded source = %q, want user", loaded.Source)
	}

	reset, err := store.Reset("user-alice", AgentProfileVText)
	if err != nil {
		t.Fatalf("reset prompt override: %v", err)
	}
	if reset.Source != "default" {
		t.Fatalf("reset source = %q, want default", reset.Source)
	}
	if reset.Content == "" || reset.Content == "Custom vtext prompt" {
		t.Fatalf("reset content should return the default prompt, got %q", reset.Content)
	}
}
