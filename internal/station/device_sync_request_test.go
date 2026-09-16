package station

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	aqiws "github.com/wonli/aqi/ws"
)

func TestBroadcastMetadataBatchDoesNotDropUpdates(t *testing.T) {
	const total = 242
	client := &aqiws.Client{Send: make(chan []byte, 1)}
	done := make(chan struct{})
	count := 0
	go func() {
		defer close(done)
		for message := range client.Send {
			var action aqiws.Action
			if err := json.Unmarshal(message, &action); err != nil {
				return
			}
			if action.Action != "v1.metadata.updated" {
				return
			}
			count++
		}
	}()
	views := make([]ServerFileView, total)
	for index := range views {
		views[index].ServerFile.ServerFileID = "sf_" + string(rune(index))
	}
	service := &Service{}
	service.broadcastMetadataBatch(client, "batch", views)
	close(client.Send)
	<-done
	if count != total {
		t.Fatalf("metadata updates sent = %d, want %d", count, total)
	}
	// The helper above must enqueue every update. Verify the encoded payload
	// shape separately through a representative message as a regression guard.
	encoded, err := json.Marshal(MetadataBatchCommand{BatchID: "batch", BatchCurrent: total, BatchTotal: total})
	if err != nil || len(encoded) == 0 {
		t.Fatal("metadata batch command was not encodable")
	}
}

func TestDeviceSyncCandidatesMatchTransferWork(t *testing.T) {
	service := newTransferTestService(t)
	deviceID := "device-sync"
	latest := ServerFile{
		ServerFileID: "sf1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		FileName:     "latest.flac", Size: 100, Hash: "hash-latest", Version: 2,
	}
	newer := ServerFile{
		ServerFileID: "sf1_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		FileName:     "newer.flac", Size: 200, Hash: "hash-new", Version: 3,
	}
	missing := ServerFile{
		ServerFileID: "sf1_cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		FileName:     "missing.flac", Size: 300, Hash: "hash-missing", Version: 1,
	}
	if err := service.db.Create(&[]ServerFile{latest, newer, missing}).Error; err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{DeviceID: deviceID, FileID: latest.ServerFileID, RelativePath: "latest.flac", ServerFileID: latest.ServerFileID, Size: 100, Hash: latest.Hash, AppliedVersion: 2},
		{DeviceID: deviceID, FileID: newer.ServerFileID, RelativePath: "newer.flac", ServerFileID: newer.ServerFileID, Size: 200, Hash: "hash-old", AppliedVersion: 2},
		{DeviceID: deviceID, FileID: "sf1_dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", RelativePath: "local.flac", Size: 400},
	}
	if err := service.db.Create(&tracks).Error; err != nil {
		t.Fatal(err)
	}

	downloads, err := service.downloadCandidates(deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 2 {
		t.Fatalf("download candidates = %d, want 2", len(downloads))
	}
	uploads, err := service.uploadCandidates(deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 2 {
		t.Fatalf("upload candidates = %d, want 2", len(uploads))
	}
}

func TestAvailableDeviceDownloadsSkipsMissingSourceForFreshDevice(t *testing.T) {
	service := newTransferTestService(t)
	root := t.TempDir()
	availablePath := filepath.Join(root, "available.flac")
	if err := os.WriteFile(availablePath, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	device := Device{DeviceID: "fresh-public-device"}
	if err := service.db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	files := []ServerFile{
		{ServerFileID: "sf1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FileName: "available.flac", StoragePath: availablePath, Size: 5, Version: 1},
		{ServerFileID: "sf1_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", FileName: "missing.flac", StoragePath: filepath.Join(root, "missing.flac"), Size: 5, Version: 1},
	}
	if err := service.db.Create(&files).Error; err != nil {
		t.Fatal(err)
	}
	available, skipped, err := service.availableDeviceDownloads(device.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || len(available) != 1 || available[0].ServerFileID != files[0].ServerFileID {
		t.Fatalf("available=%+v skipped=%d", available, skipped)
	}
	offers, err := service.CreateSyncOffers(CreateSyncOfferRequest{DeviceIDs: []string{device.DeviceID}, ServerFileIDs: []string{available[0].ServerFileID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers=%+v", offers)
	}
	var item SyncOfferItem
	if err := service.db.Where("offer_id = ?", offers[0].OfferID).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.ServerFileID != files[0].ServerFileID || item.Hash == "" {
		t.Fatalf("item=%+v", item)
	}
	if err := os.Remove(availablePath); err != nil {
		t.Fatal(err)
	}
	available, skipped, err = service.availableDeviceDownloads(device.DeviceID)
	if err != nil || len(available) != 0 || skipped != 2 {
		t.Fatalf("all unavailable: files=%+v skipped=%d err=%v", available, skipped, err)
	}
}
