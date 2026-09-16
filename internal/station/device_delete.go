package station

import (
	"errors"
	"io"
	"strings"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

func (s *Service) DeleteDevice(deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("device id is required")
	}
	var device Device
	if err := s.db.Where("device_id = ?", deviceID).First(&device).Error; err != nil {
		return err
	}

	var activeTasks []TransferTask
	if err := s.db.Where(
		"device_id = ? AND state IN ?",
		deviceID,
		[]string{"prepared", "uploading", "downloading", "paused"},
	).Find(&activeTasks).Error; err != nil {
		return err
	}
	for i := range activeTasks {
		s.cancelledTransfers.Store(activeTasks[i].TaskID, true)
		if value, ok := s.activeTransfers.Load(activeTasks[i].TaskID); ok {
			if connection, ok := value.(io.Closer); ok {
				_ = connection.Close()
			}
		}
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&TransferTask{}).Where(
			"device_id = ? AND state IN ?",
			deviceID,
			[]string{"prepared", "uploading", "downloading", "paused"},
		).Updates(map[string]any{"state": "cancelled", "last_error": "device removed"}).Error; err != nil {
			return err
		}
		if err := tx.Where("device_id = ?", deviceID).Delete(&Track{}).Error; err != nil {
			return err
		}
		return tx.Where("device_id = ?", deviceID).Delete(&Device{}).Error
	}); err != nil {
		return err
	}

	if value, ok := s.onlineDevices.LoadAndDelete(deviceID); ok {
		if client, ok := value.(*ws.Client); ok && client != nil {
			client.Close()
		}
	}
	s.emitDeviceChanged(deviceID)
	return nil
}
