package station

import "testing"

func TestSyncQueueDraftDoesNotCreateOfferUntilDispatched(t *testing.T) {
	service := newTransferTestService(t)
	device := Device{DeviceID: "queue-device", Name: "Phone"}
	file := ServerFile{ServerFileID: "sf1_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", FileName: "song.flac", RelativePath: "Album/song.flac", Size: 42, Hash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
	if err := service.db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}

	item, err := service.AddSyncQueueItem(SyncQueueAddRequest{DeviceID: device.DeviceID, ServerFileID: file.ServerFileID})
	if err != nil {
		t.Fatal(err)
	}
	if item.QueueItemID == "" {
		t.Fatal("queue item id was not generated")
	}
	var offerCount int64
	if err := service.db.Model(&SyncOffer{}).Count(&offerCount).Error; err != nil {
		t.Fatal(err)
	}
	if offerCount != 0 {
		t.Fatalf("draft queue must not create offers, got %d", offerCount)
	}

	offers, err := service.DispatchSyncQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 {
		t.Fatalf("expected one dispatched offer, got %+v", offers)
	}
	items, err := service.ListSyncQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("queue should be empty after dispatch: %+v", items)
	}
}
