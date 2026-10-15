package scheduler

import "testing"

func TestSessionIDExplicitHeaderIsUserScoped(t *testing.T) {
	got := SessionID("conversation", 7, 11, nil)
	other := SessionID("conversation", 8, 11, nil)
	if got == other || got == "" {
		t.Fatalf("explicit session header leaked across users: %q %q", got, other)
	}
	if got != SessionID("conversation", 7, 11, nil) {
		t.Fatal("explicit session must be deterministic")
	}
}

func TestSessionIDFallbackIsUserAndTokenScoped(t *testing.T) {
	messages := []Message{{Role: "user", Content: "hello"}}
	first := SessionID("", 7, 11, messages)
	if first == SessionID("", 8, 11, messages) || first == SessionID("", 7, 12, messages) {
		t.Fatalf("fallback session is not scoped: %q", first)
	}
	if first != SessionID("", 7, 11, messages) {
		t.Fatal("fallback session is not deterministic")
	}
}
