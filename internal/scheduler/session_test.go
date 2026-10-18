package scheduler

import "testing"

func TestSessionIDExplicitHeaderIsUserScoped(t *testing.T) {
	got := SessionKey("conversation", 7, 11, "")
	other := SessionKey("conversation", 8, 11, "")
	if got == other || got == "" {
		t.Fatalf("explicit session header leaked across users: %q %q", got, other)
	}
	if got != SessionKey("conversation", 7, 11, "") {
		t.Fatal("explicit session must be deterministic")
	}
}

func TestSessionIDFallbackIsUserAndTokenScoped(t *testing.T) {
	firstMessage := "hello"
	first := SessionKey("", 7, 11, firstMessage)
	if first == SessionKey("", 8, 11, firstMessage) || first == SessionKey("", 7, 12, firstMessage) {
		t.Fatalf("fallback session is not scoped: %q", first)
	}
	if first != SessionKey("", 7, 11, firstMessage) {
		t.Fatal("fallback session is not deterministic")
	}
}
