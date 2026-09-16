package station

import "github.com/wonli/aqi/ws"

func (s *Service) SyncStatus(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var status struct {
		FileID string `json:"fileId"`
		State  string `json:"state"`
	}
	if !decodeParams(a, &status) || status.FileID == "" {
		a.SendCode(400, "invalid sync status")
		return
	}
	s.db.Model(&Track{}).Where("device_id = ? AND file_id = ?", deviceID, status.FileID).Update("sync_state", status.State)
	a.Send(map[string]any{"accepted": true})
}
