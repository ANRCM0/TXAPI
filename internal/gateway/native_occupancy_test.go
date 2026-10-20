package gateway

import (
	"context"
	"sync"
	"testing"

	contract "minigateway/internal/foundation/contracts/responsesws/v1"
)

func TestNativeOccupancyTracksBusyRoundAndCloseJoin(t *testing.T) {
	f := newNativeServiceFixture(t)
	f.block = make(chan struct{})
	f.publish(t)
	s := f.open(t, "occupancy-session")
	if got := f.service.Occupancy(); got != (NativeWSOccupancy{Connections: 1}) {
		t.Fatal(got)
	}
	result := make(chan contract.Result, 1)
	go func() { result <- s.Execute(f.trusted, nativeCreate(""), nativeDiscard) }()
	resourceWait(t, f.resources[0].conn.entered)
	if got := f.service.Occupancy(); got != (NativeWSOccupancy{Connections: 1, Rounds: 1}) {
		t.Fatal(got)
	}
	if r := s.Execute(f.trusted, nativeCreate(""), nativeDiscard); r.Problem == nil || r.Problem.Code != "busy" {
		t.Fatal("concurrent round was not rejected")
	}
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 100; j++ {
				got := f.service.Occupancy()
				if got.Connections < 0 || got.Connections > 1 || got.Rounds < 0 || got.Rounds > got.Connections {
					t.Errorf("invalid concurrent snapshot: %+v", got)
					return
				}
			}
		}()
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	<-result
	if got := f.service.Occupancy(); got != (NativeWSOccupancy{}) {
		t.Fatal("closed resources still counted", got)
	}
}
