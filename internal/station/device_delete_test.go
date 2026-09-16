package station

import "testing"

func TestDeleteDeviceRemovesInventoryAndKeepsServerLibrary(t *testing.T) {
	service := newTransferTestService(t)
	device := Device{DeviceID: "device-old", Name: "Local"}
	track := Track{DeviceID: device.DeviceID, FileID: "file-a", RelativePath: "Album/song.mp3", ServerFileID: "sf-a"}
	serverFile := ServerFile{ServerFileID: "sf-a", FileName: "song.mp3", Version: 1}
	task := TransferTask{TaskID: "tr-a", DeviceID: device.DeviceID, Direction: "download", State: "paused"}
	for _, value := range []any{&device, &track, &serverFile, &task} {
		if err := service.db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := service.DeleteDevice(device.DeviceID); err != nil {
		t.Fatal(err)
	}
	var deviceCount, trackCount, serverFileCount int64
	service.db.Model(&Device{}).Where("device_id = ?", device.DeviceID).Count(&deviceCount)
	service.db.Model(&Track{}).Where("device_id = ?", device.DeviceID).Count(&trackCount)
	service.db.Model(&ServerFile{}).Where("server_file_id = ?", serverFile.ServerFileID).Count(&serverFileCount)
	if deviceCount != 0 || trackCount != 0 || serverFileCount != 1 {
		t.Fatalf("unexpected delete result: devices=%d tracks=%d serverFiles=%d", deviceCount, trackCount, serverFileCount)
	}
	var updatedTask TransferTask
	if err := service.db.Where("task_id = ?", task.TaskID).First(&updatedTask).Error; err != nil {
		t.Fatal(err)
	}
	if updatedTask.State != "cancelled" {
		t.Fatalf("task state = %q", updatedTask.State)
	}
}
