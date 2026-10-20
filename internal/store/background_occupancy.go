package store

import (
	"context"
	"errors"
)

// BackgroundOccupancy contains retained metadata only. It is an observation,
// not task authority, and includes tombstones until metadata retention ends.
type BackgroundOccupancy struct {
	Queued, Dispatching, RemoteActive, Terminal, Uncertain, Other int64
	ReplayPending, Events, EventBytes, ReservedBytes              int64
}

func (s *Store) BackgroundOccupancy(ctx context.Context) (BackgroundOccupancy, error) {
	var row struct {
		BackgroundOccupancy
		Records int64
		Invalid int64
	}
	// One statement observes one database snapshot. The guard row detects an
	// invalid record count without scanning an unbounded or corrupted table.
	err := s.db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS records,
 COALESCE(SUM(CASE WHEN stage = 'queued' THEN 1 ELSE 0 END), 0) AS queued,
 COALESCE(SUM(CASE WHEN stage = 'dispatching' THEN 1 ELSE 0 END), 0) AS dispatching,
 COALESCE(SUM(CASE WHEN stage = 'remote_active' THEN 1 ELSE 0 END), 0) AS remote_active,
 COALESCE(SUM(CASE WHEN stage = 'terminal' THEN 1 ELSE 0 END), 0) AS terminal,
 COALESCE(SUM(CASE WHEN stage = 'uncertain' THEN 1 ELSE 0 END), 0) AS uncertain,
 COALESCE(SUM(CASE WHEN stage NOT IN ('queued', 'dispatching', 'remote_active', 'terminal', 'uncertain') THEN 1 ELSE 0 END), 0) AS other,
 COALESCE(SUM(CASE WHEN replay_pending <> 0 THEN 1 ELSE 0 END), 0) AS replay_pending,
 COALESCE(SUM(event_count), 0) AS events,
 COALESCE(SUM(stored_bytes), 0) AS event_bytes,
 COALESCE(SUM(reserved_bytes), 0) AS reserved_bytes,
 COALESCE(SUM(CASE WHEN event_count < 0 OR stored_bytes < 0 OR reserved_bytes < 0 THEN 1 ELSE 0 END), 0) AS invalid
FROM (SELECT stage, replay_pending, event_count, stored_bytes, reserved_bytes FROM background_jobs LIMIT 4097)
`).Scan(&row).Error
	if err != nil {
		return BackgroundOccupancy{}, err
	}
	if row.Records > 4096 || row.Invalid != 0 {
		return BackgroundOccupancy{}, errors.New("background occupancy metadata exceeds valid bounds")
	}
	return row.BackgroundOccupancy, nil
}
