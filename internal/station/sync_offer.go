package station

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

type CreateSyncOfferRequest struct {
	TargetLibraryID string   `json:"targetLibraryId,omitempty"`
	DeviceIDs       []string `json:"deviceIds"`
	ServerFileIDs   []string `json:"serverFileIds"`
}

type SyncOfferCommand struct {
	TargetLibraryID string          `json:"targetLibraryId,omitempty"`
	OfferID         string          `json:"offerId"`
	Items           []SyncOfferItem `json:"items"`
}

type SyncOfferResponse struct {
	ExistingFileIDs []string `json:"existingFileIds,omitempty"`
	Error           string   `json:"error,omitempty"`
	OfferID         string   `json:"offerId"`
	Choice          string   `json:"choice"`
}

func (s *Service) CreateSyncOffers(request CreateSyncOfferRequest) ([]SyncOffer, error) {
	if len(request.DeviceIDs) == 0 || len(request.ServerFileIDs) == 0 || len(request.DeviceIDs) > 32 || len(request.ServerFileIDs) > 500 {
		return nil, errors.New("invalid sync selection")
	}
	var files []ServerFile
	if err := s.db.Where("server_file_id IN ? AND deleted_at IS NULL", request.ServerFileIDs).Find(&files).Error; err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("no server files selected")
	}
	offers := make([]SyncOffer, 0, len(request.DeviceIDs))
	for _, deviceID := range request.DeviceIDs {
		var device Device
		if err := s.db.Where("device_id = ?", deviceID).First(&device).Error; err != nil {
			return offers, errors.New("target device is unknown")
		}
		offer := SyncOffer{TargetLibraryID: request.TargetLibraryID, OfferID: newID("so"), DeviceID: deviceID, State: "queued"}
		items := make([]SyncOfferItem, 0, len(files))
		for index := range files {
			file := &files[index]
			var existing Track
			found := s.db.Where("device_id = ? AND server_file_id = ?", deviceID, file.ServerFileID).Limit(1).Find(&existing)
			if found.Error != nil {
				return offers, found.Error
			}
			if request.TargetLibraryID == "" && found.RowsAffected > 0 && existing.AppliedVersion >= file.Version && existing.Hash == file.Hash {
				continue
			}
			if err := s.ensureServerFileHash(file); err != nil {
				return offers, fmt.Errorf("prepare download audio %s (%s): %w", file.ServerFileID, file.FileName, err)
			}
			items = append(items, SyncOfferItem{
				OfferID: offer.OfferID, ServerFileID: file.ServerFileID,
				RelativePath: file.RelativePath, FileName: file.FileName, Size: file.Size,
				Hash: file.Hash, Title: file.Title, Artist: file.Artist, Album: file.Album, Format: file.Format,
			})
		}
		if len(items) == 0 {
			s.broadcastMetadataForDevice(deviceID, files)
			continue
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&offer).Error; err != nil {
				return err
			}
			return tx.Create(&items).Error
		}); err != nil {
			return offers, err
		}
		s.sendSyncOffer(offer, items)
		offers = append(offers, offer)
	}
	return offers, nil
}

func (s *Service) sendSyncOffer(offer SyncOffer, items []SyncOfferItem) bool {
	value, ok := s.onlineDevices.Load(offer.DeviceID)
	client, valid := value.(*ws.Client)
	if !ok || !valid || client == nil {
		return false
	}
	if offer.State == "queued" {
		if s.db.Model(&SyncOffer{}).Where("offer_id = ?", offer.OfferID).Update("state", "offered").Error != nil {
			return false
		}
	}
	client.SendActionMsg(&ws.Action{Action: "v1.sync.offer", Data: SyncOfferCommand{TargetLibraryID: offer.TargetLibraryID, OfferID: offer.OfferID, Items: items}})
	return true
}

// DispatchPendingSyncOffers makes Station queues durable across device disconnects.
// An offered item is intentionally sent again after reconnect until the device
// records a choice, so a lost WebSocket message cannot lose the user's request.
func (s *Service) DispatchPendingSyncOffers(deviceID string) {
	var offers []SyncOffer
	if err := s.db.Where("device_id = ? AND state IN ?", deviceID, []string{"queued", "offered"}).Order("created_at ASC").Find(&offers).Error; err != nil {
		return
	}
	for _, offer := range offers {
		var items []SyncOfferItem
		if s.db.Where("offer_id = ?", offer.OfferID).Order("id ASC").Find(&items).Error == nil {
			s.sendSyncOffer(offer, items)
		}
	}
}

// SyncDeviceMetadata sends the Station-selected resources for audio already on
// the device. This also catches metadata edits made while the device was offline.
func (s *Service) SyncDeviceMetadata(deviceID string) {
	var files []ServerFile
	_ = s.db.Distinct("server_files.*").
		Joins("JOIN tracks ON tracks.server_file_id = server_files.server_file_id").
		Where("tracks.device_id = ? AND server_files.deleted_at IS NULL", deviceID).
		Find(&files).Error
	s.broadcastMetadataForDevice(deviceID, files)
}

func (s *Service) RespondSyncOffer(a *ws.Context) {
	deviceID := clientDeviceID(a)
	if deviceID == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var response SyncOfferResponse
	if !decodeParams(a, &response) || (response.Choice != "download" && response.Choice != "remote" && response.Choice != "later" && response.Choice != "reject") {
		a.SendCode(400, "invalid sync offer response")
		return
	}
	if err := s.respondSyncOffer(deviceID, response); err != nil {
		a.SendCode(400, err.Error())
		return
	}
	a.Send(map[string]any{"accepted": true})
}

func (s *Service) respondSyncOffer(deviceID string, response SyncOfferResponse) error {
	var offer SyncOffer
	if err := s.db.Where("offer_id = ? AND device_id = ?", response.OfferID, deviceID).First(&offer).Error; err != nil {
		return err
	}
	if offer.State == "accepted" || offer.State == "completed" {
		return nil
	}
	if offer.State != "offered" {
		return errors.New("sync offer is no longer pending")
	}
	var items []SyncOfferItem
	if err := s.db.Where("offer_id = ?", offer.OfferID).Find(&items).Error; err != nil {
		return err
	}
	if response.Choice == "reject" {
		if err := s.db.Model(&offer).Updates(map[string]any{"state": "rejected", "choice": "reject"}).Error; err != nil {
			return err
		}
		s.broadcastEvent(StationEvent{Kind: "sync_rejected", DeviceID: deviceID, Error: response.Error})
		return nil
	}
	if offer.TargetLibraryID != "" && response.Choice != "download" {
		return errors.New("target library requires download")
	}
	if offer.TargetLibraryID != "" {
		existing := map[string]bool{}
		for _, id := range response.ExistingFileIDs {
			existing[id] = true
		}
		missing := make([]SyncOfferItem, 0, len(items))
		for _, item := range items {
			if !existing[item.ServerFileID] {
				missing = append(missing, item)
			}
		}
		items = missing
	}
	offer.Choice = response.Choice
	offer.State = "accepted"
	if response.Choice == "later" {
		offer.State = "deferred"
	}
	if response.Choice == "remote" {
		offer.State = "completed"
	}
	if response.Choice == "download" && len(items) == 0 {
		offer.State = "completed"
	}
	if err := s.db.Save(&offer).Error; err != nil {
		return err
	}
	if response.Choice != "download" {
		if response.Choice == "remote" {
			var files []ServerFile
			ids := make([]string, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.ServerFileID)
			}
			if err := s.db.Where("server_file_id IN ?", ids).Find(&files).Error; err == nil {
				s.broadcastMetadataForDevice(deviceID, files)
			}
		}
		return nil
	}
	if len(items) == 0 {
		return nil
	}
	var batchBytes int64
	for _, item := range items {
		batchBytes += item.Size
	}
	if err := s.createTransferBatch(offer.OfferID, deviceID, "download", len(items), batchBytes); err != nil {
		return err
	}
	for _, item := range items {
		track := Track{
			DeviceID: deviceID, FileID: item.ServerFileID, ServerFileID: item.ServerFileID,
			RelativePath: cleanRelativePath(item.RelativePath), Folder: filepathDir(item.RelativePath),
			FileName: item.FileName, Size: item.Size, Title: item.Title, Artist: item.Artist,
			Album: item.Album, Format: item.Format, Hash: item.Hash, SyncState: "downloading",
		}
		if err := s.upsertTracks(deviceID, "", []Track{track}); err != nil {
			return err
		}
		task := TransferTask{
			TaskID: newID("tr"), ServerFileID: item.ServerFileID, DeviceID: deviceID,
			BatchID:   offer.OfferID,
			Direction: "download", FileID: item.ServerFileID, RelativePath: item.RelativePath,
			FileName: item.FileName, ExpectedHash: item.Hash, TotalBytes: item.Size,
			TargetVersion: 1, State: "prepared",
		}
		if err := s.db.Create(&task).Error; err != nil {
			return err
		}
	}
	return s.dispatchNextDownload(deviceID)
}

func filepathDir(relativePath string) string {
	directory := cleanRelativePath(relativePath)
	if index := strings.LastIndex(directory, "/"); index >= 0 {
		return directory[:index]
	}
	return ""
}

func (s *Service) broadcastMetadataForDevice(deviceID string, files []ServerFile) {
	value, ok := s.onlineDevices.Load(deviceID)
	client, valid := value.(*ws.Client)
	if !ok || !valid || client == nil {
		return
	}
	if client.GetKey(metadataCursorCapability).String() == "1" {
		if s.enqueueDeviceMetadata(deviceID, files) != nil {
			return
		}
		s.notifyMetadataCursor(client)
		return
	}
	for _, file := range files {
		if view, err := s.metadataPushView(file.ServerFileID); err == nil {
			client.SendActionMsg(&ws.Action{Action: "v1.metadata.updated", Data: view})
		}
	}
}
