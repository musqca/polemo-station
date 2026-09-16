package station

import "github.com/wonli/aqi/ws"

func (s *Service) LibraryUpsert(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var track Track
	if !decodeParams(a, &track) || track.FileID == "" {
		a.SendCode(400, "invalid file")
		return
	}
	if err := s.upsertTracks(deviceID, "", []Track{track}); err != nil {
		a.SendCode(500, "upsert failed")
		return
	}
	a.Send(map[string]any{"accepted": 1})
	s.emitLibraryChanged(deviceID)
}
