package gateway

import "testing"

func testCoordinator(t *testing.T) *Gateway {
	t.Helper()
	g, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}
