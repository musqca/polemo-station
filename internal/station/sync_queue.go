package station

import (
	"errors"

	"gorm.io/gorm"
)

type SyncQueueAddRequest struct {
	DeviceID     string `json:"deviceId"`
	ServerFileID string `json:"serverFileId"`
}

func (s *Service) ListSyncQueue() ([]SyncQueueItem, error) {
	var items []SyncQueueItem
	if err := s.db.Order("created_at asc").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) AddSyncQueueItem(request SyncQueueAddRequest) (SyncQueueItem, error) {
	if request.DeviceID == "" || request.ServerFileID == "" {
		return SyncQueueItem{}, errors.New("invalid sync queue item")
	}
	var device Device
	if err := s.db.Where("device_id = ?", request.DeviceID).First(&device).Error; err != nil {
		return SyncQueueItem{}, errors.New("target device is unknown")
	}
	var file ServerFile
	if err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", request.ServerFileID).First(&file).Error; err != nil {
		return SyncQueueItem{}, errors.New("server file is unknown")
	}
	item := SyncQueueItem{QueueItemID: newID("sq"), DeviceID: request.DeviceID, ServerFileID: request.ServerFileID}
	if err := s.db.Where("device_id = ? AND server_file_id = ?", item.DeviceID, item.ServerFileID).FirstOrCreate(&item).Error; err != nil {
		return SyncQueueItem{}, err
	}
	s.emitSyncQueueChanged()
	return item, nil
}

func (s *Service) RemoveSyncQueueItem(queueItemID string) error {
	if queueItemID == "" {
		return errors.New("invalid sync queue item")
	}
	result := s.db.Where("queue_item_id = ?", queueItemID).Delete(&SyncQueueItem{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	s.emitSyncQueueChanged()
	return nil
}

func (s *Service) DispatchSyncQueue() ([]SyncOffer, error) {
	items, err := s.ListSyncQueue()
	if err != nil || len(items) == 0 {
		return nil, err
	}
	byDevice := map[string][]string{}
	for _, item := range items {
		byDevice[item.DeviceID] = append(byDevice[item.DeviceID], item.ServerFileID)
	}
	offers := make([]SyncOffer, 0)
	for deviceID, fileIDs := range byDevice {
		created, createErr := s.CreateSyncOffers(CreateSyncOfferRequest{DeviceIDs: []string{deviceID}, ServerFileIDs: fileIDs})
		if createErr != nil {
			return offers, createErr
		}
		offers = append(offers, created...)
	}
	if err := s.db.Where("queue_item_id IN ?", queueIDs(items)).Delete(&SyncQueueItem{}).Error; err != nil {
		return offers, err
	}
	s.emitSyncQueueChanged()
	return offers, nil
}

func queueIDs(items []SyncQueueItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.QueueItemID)
	}
	return ids
}
