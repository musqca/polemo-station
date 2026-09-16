package station

import "testing"

func TestHideDeviceFromListKeepsDeviceData(t *testing.T) {
	service := newTransferTestService(t)
	device := Device{DeviceID: "device-hidden", Name: "Phone"}
	track := Track{DeviceID: device.DeviceID, FileID: "file-a", RelativePath: "Album/song.mp3"}
	if err := service.db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&track).Error; err != nil {
		t.Fatal(err)
	}

	if err := service.SetDevicePinned(device.DeviceID, true); err != nil {
		t.Fatal(err)
	}
	if err := service.HideDeviceFromList(device.DeviceID); err != nil {
		t.Fatal(err)
	}

	devices, err := service.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Fatalf("visible devices = %d, want 0", len(devices))
	}
	var stored Device
	if err := service.db.Where("device_id = ?", device.DeviceID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.Hidden || stored.Pinned {
		t.Fatalf("hidden=%v pinned=%v, want hidden and unpinned", stored.Hidden, stored.Pinned)
	}
	var trackCount int64
	service.db.Model(&Track{}).Where("device_id = ?", device.DeviceID).Count(&trackCount)
	if trackCount != 1 {
		t.Fatalf("tracks = %d, want 1", trackCount)
	}
}

func TestListDevicesPlacesPinnedDevicesFirst(t *testing.T) {
	service := newTransferTestService(t)
	for _, device := range []Device{{DeviceID: "device-a"}, {DeviceID: "device-b"}} {
		if err := service.db.Create(&device).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := service.SetDevicePinned("device-b", true); err != nil {
		t.Fatal(err)
	}
	devices, err := service.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].DeviceID != "device-b" || !devices[0].Pinned {
		t.Fatalf("unexpected device order: %#v", devices)
	}
}

func TestListDevicesIncludesLegacyNullHiddenValue(t *testing.T) {
	service := newTransferTestService(t)
	device := Device{DeviceID: "legacy-device"}
	if err := service.db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Exec("UPDATE devices SET hidden = NULL WHERE device_id = ?", device.DeviceID).Error; err != nil {
		t.Fatal(err)
	}

	devices, err := service.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceID != device.DeviceID {
		t.Fatalf("legacy device was not visible: %#v", devices)
	}
	if err := service.HideDeviceFromList(device.DeviceID); err != nil {
		t.Fatalf("hide legacy device: %v", err)
	}
	devices, err = service.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Fatalf("hidden legacy device was still visible: %#v", devices)
	}
}
