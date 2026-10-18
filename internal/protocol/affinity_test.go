package protocol

import "testing"

func TestAffinityProjectionPreservesFirstUserAndMultimodalText(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"model":"m","messages":[{"role":"system","content":"ignore"},{"role":"user","content":"first"},{"role":"user","content":"later"}]}`, "first"},
		{`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"fixture"}},{"type":"text","text":" part"}]}]}`, "first part"},
		{`{"model":"m","messages":[{"role":"user","content":[]},{"role":"user","content":"next text"}]}`, "next text"},
		{`{"model":"m","messages":[{"role":"assistant","content":"no user"}]}`, ""},
	} {
		r, err := ParseRequest(Chat, []byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if got := r.AffinityText(); got != tc.want {
			t.Fatalf("affinity=%q want=%q", got, tc.want)
		}
	}
}
