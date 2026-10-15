package access

import "testing"

func TestExplicitPoliciesAndIntersection(t *testing.T) {
	all := Policy{Mode: All}
	a := Policy{Mode: Allowlist, Models: []string{"m1", "m2"}}
	b := Policy{Mode: Allowlist, Models: []string{"m1"}}
	deny := Policy{Mode: Allowlist, Models: []string{}}
	for _, p := range []Policy{Intersect(all, a, b), Intersect(b, all, a)} {
		if !p.Allows("m1") || p.Allows("m2") {
			t.Fatalf("intersection=%+v", p)
		}
	}
	if Intersect(all, deny).Allows("m1") {
		t.Fatal("empty allowlist became unrestricted")
	}
	if Subset(all, a) || Subset(a, b) || !Subset(b, a) || !Subset(deny, all) {
		t.Fatal("incorrect narrowing")
	}
	if (Policy{}).Allows("m1") || (Policy{Mode: Allowlist, Models: []string{"*"}}).Validate() == nil {
		t.Fatal("ambiguous model policy accepted")
	}
}
func TestGeneratedSecretsAndPasswords(t *testing.T) {
	a, err := Secret("sk-txapi-")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Secret("sk-txapi-")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) < 50 || Hash(a) == a || len(Hash(a)) != 64 {
		t.Fatal("invalid secret generation")
	}
	hash, err := PasswordHash("a-test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "a-test-password-123" || !PasswordMatches(hash, "a-test-password-123") || PasswordMatches(hash, "incorrect") {
		t.Fatal("password verification failed")
	}
}
