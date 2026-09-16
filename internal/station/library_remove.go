package station

import "github.com/wonli/aqi/ws"

func (s *Service) LibraryRemove(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var remove struct {
		FileID string `json:"fileId"`
	}
	if !decodeParams(a, &remove) || remove.FileID == "" {
		a.SendCode(400, "invalid file")
		return
	}
	if err := s.db.Where("device_id = ? AND file_id = ?", deviceID, remove.FileID).Delete(&Track{}).Error; err != nil {
		a.SendCode(500, "remove failed")
		return
	}
	a.Send(map[string]any{"removed": remove.FileID})
	s.emitLibraryChanged(deviceID)
}
