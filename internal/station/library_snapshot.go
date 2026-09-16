package station

import "github.com/wonli/aqi/ws"

type snapshotPayload struct {
	SnapshotID string  `json:"snapshotId"`
	Sequence   int     `json:"sequence"`
	Total      int     `json:"total"`
	Files      []Track `json:"files"`
}

func (s *Service) LibrarySnapshot(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var snapshot snapshotPayload
	if !decodeParams(a, &snapshot) || len(snapshot.Files) > 500 {
		a.SendCode(400, "invalid snapshot")
		return
	}
	if err := s.upsertTracks(deviceID, snapshot.SnapshotID, snapshot.Files); err != nil {
		a.SendCode(500, "snapshot failed")
		return
	}
	if snapshot.Total == 0 || snapshot.Sequence == snapshot.Total-1 {
		query := s.db.Where("device_id = ?", deviceID)
		if snapshot.SnapshotID != "" {
			query = query.Where("snapshot_id <> ? OR snapshot_id = ''", snapshot.SnapshotID)
		}
		if err := query.Delete(&Track{}).Error; err != nil {
			a.SendCode(500, "snapshot commit failed")
			return
		}
		s.emitLibraryChanged(deviceID)
	}
	a.Send(map[string]any{"accepted": len(snapshot.Files), "sequence": snapshot.Sequence, "total": snapshot.Total})
}
