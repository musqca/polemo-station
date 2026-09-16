package station

import "testing"

func TestCreateSyncOfferQueuesForOfflineDevice(t *testing.T) {
	service := newTransferTestService(t)
	device := Device{DeviceID: "offline-device", Name: "Phone", Online: false}
	file := ServerFile{
		ServerFileID: "sf1_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		FileName:     "song.flac", RelativePath: "Album/song.flac", Size: 42,
		Hash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	if err := service.db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}

	offers, err := service.CreateSyncOffers(CreateSyncOfferRequest{
		DeviceIDs: []string{device.DeviceID}, ServerFileIDs: []string{file.ServerFileID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].State != "queued" {
		t.Fatalf("unexpected offline offers: %+v", offers)
	}
	var item SyncOfferItem
	if err := service.db.Where("offer_id = ?", offers[0].OfferID).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.RelativePath != file.RelativePath || item.ServerFileID != file.ServerFileID {
		t.Fatalf("unexpected queued item: %+v", item)
	}
}

func TestCreateSyncOfferSkipsAudioAlreadyOnDevice(t *testing.T) {
	service := newTransferTestService(t)
	device := Device{DeviceID: "device-a", Name: "Phone"}
	file := ServerFile{ServerFileID: "sf1_dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", FileName: "song.wav"}
	track := Track{DeviceID: device.DeviceID, FileID: file.ServerFileID, RelativePath: "song.wav", ServerFileID: file.ServerFileID}
	if err := service.db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&track).Error; err != nil {
		t.Fatal(err)
	}
	offers, err := service.CreateSyncOffers(CreateSyncOfferRequest{
		DeviceIDs: []string{device.DeviceID}, ServerFileIDs: []string{file.ServerFileID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 0 {
		t.Fatalf("metadata-only sync must not prompt for audio: %+v", offers)
	}
}
