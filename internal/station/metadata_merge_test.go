package station

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestMergedResourceRejectsLyricsHashWithSource(t *testing.T) {
	lyrics := "[00:01.00]Test lyrics content"

	for _, source := range []string{"embedded", "sidecar"} {
		t.Run(source, func(t *testing.T) {
			hash := sha256.Sum256([]byte("lyrics\x00" + source + "\x00lrc\x00\x00" + lyrics))
			_, _, err := mergedResource("sf1_example", "test-device", ResourceMergeInput{
				Type:         "lyrics",
				ResourceHash: hex.EncodeToString(hash[:]),
				Data:         lyrics,
				Source:       source,
				Format:       "lrc",
			})
			if err == nil || err.Error() != "resource hash mismatch" {
				t.Fatalf("expected resource hash mismatch, got %v", err)
			}
		})
	}
}

func TestMergeDeviceResourcesBatchEndToEnd(t *testing.T) {
	service := newTransferTestService(t)
	serverFileID := "sf1_2222222222222222222222222222222222222222222222222222222222222222"
	deviceID := "test-device"

	// Shared with the App fixture in station_metadata_resource_test.dart.
	lyricsText := "\ufeff  [00:01.00]Line one\r\n[00:02.00]Line two  "
	const lyricsHash = "c233537bc958c541e0bb137174ab4b66d6408946b8d1cc8d6ef0ef88d7b6153f"

	metaJSON := `{"title":"Song Title","artist":"Singer"}`
	metaHashInput := "metadata\x00json\x00\x00" + metaJSON
	metaSum := sha256.Sum256([]byte(metaHashInput))
	metaHash := hex.EncodeToString(metaSum[:])

	req := ResourceMergeRequest{
		ServerFileID: serverFileID,
		Resources: []ResourceMergeInput{
			{
				Type:         "metadata",
				ResourceHash: metaHash,
				Data:         metaJSON,
				Source:       "embedded",
				Format:       "json",
				Language:     "",
			},
			{
				Type:         "lyrics",
				ResourceHash: lyricsHash,
				Data:         lyricsText,
				Source:       "embedded",
				Format:       "lrc",
				Language:     "",
			},
		},
	}

	accepted, err := service.mergeDeviceResources(deviceID, req)
	if err != nil {
		t.Fatalf("mergeDeviceResources failed: %v", err)
	}
	if accepted != 2 {
		t.Fatalf("expected 2 accepted resources, got %d", accepted)
	}

	// Verify both resources in db
	var resources []MetadataResource
	if err := service.db.Where("server_file_id = ?", serverFileID).Find(&resources).Error; err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources in DB, got %d", len(resources))
	}

	// Identical lyrics from another source map to the same stored resource.
	req.Resources[1].Source = "sidecar"
	acceptedSecond, err := service.mergeDeviceResources(deviceID, req)
	if err != nil {
		t.Fatalf("second mergeDeviceResources failed: %v", err)
	}
	if acceptedSecond != 0 {
		t.Fatalf("expected 0 accepted resources on second run, got %d", acceptedSecond)
	}

	if err := service.db.Where("server_file_id = ?", serverFileID).Find(&resources).Error; err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources after syncing another source, got %d", len(resources))
	}

	differentLyrics := "[00:01.00]Alternate lyrics"
	differentHash := sha256.Sum256([]byte("lyrics\x00lrc\x00\x00" + differentLyrics))
	req.Resources[1].Data = differentLyrics
	req.Resources[1].ResourceHash = hex.EncodeToString(differentHash[:])
	acceptedThird, err := service.mergeDeviceResources(deviceID, req)
	if err != nil || acceptedThird != 1 {
		t.Fatalf("expected the different lyrics to be stored, got %d accepted: %v", acceptedThird, err)
	}
}
