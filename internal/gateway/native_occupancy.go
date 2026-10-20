package gateway

// NativeWSOccupancy describes authorized sessions and active Execute calls.
// Sessions remain counted until Close has joined their active round and freed
// its resources. These are not TCP connection or upstream pool counts.
type NativeWSOccupancy struct {
	Connections int
	Rounds      int
}

// Occupancy reads only owner state. The session count is bounded by the
// service's connection capacity; no network or store operations are performed.
func (s *NativeWebSocketService) Occupancy() NativeWSOccupancy {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := NativeWSOccupancy{Connections: len(s.sessions)}
	for _, session := range s.sessions {
		session.mu.Lock()
		if session.active {
			out.Rounds++
		}
		session.mu.Unlock()
	}
	return out
}
