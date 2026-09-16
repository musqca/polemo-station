package station

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// SetDevicePinned only changes the order in which a device is presented in
// Station. It deliberately has no effect on its connection or authorization.
func (s *Service) SetDevicePinned(deviceID string, pinned bool) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("device id is required")
	}
	result := s.db.Model(&Device{}).Where("device_id = ? AND (hidden = ? OR hidden IS NULL)", deviceID, false).Update("pinned", pinned)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	s.emitDeviceChanged(deviceID)
	return nil
}

// HideDeviceFromList removes a device from Station's visible device list while
// retaining its inventory, transfer history, authorization, and live socket.
// A later device hello makes it visible again.
func (s *Service) HideDeviceFromList(deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("device id is required")
	}
	result := s.db.Model(&Device{}).Where("device_id = ? AND (hidden = ? OR hidden IS NULL)", deviceID, false).
		Updates(map[string]any{"hidden": true, "pinned": false})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	s.emitDeviceChanged(deviceID)
	return nil
}
