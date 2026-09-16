package station

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func cursorTestSong(t *testing.T, s *Service, number int, device string) ServerFile {
	t.Helper()
	file := ServerFile{ServerFileID: fmt.Sprintf("sf1_%064x", number), FileName: fmt.Sprintf("%d.flac", number), Title: fmt.Sprintf("Song %d", number)}
	if err := s.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if device != "" {
		cursorTestAttach(t, s, file, device)
	}
	return file
}
func cursorTestAttach(t *testing.T, s *Service, file ServerFile, device string) {
	t.Helper()
	track := Track{DeviceID: device, FileID: file.ServerFileID, ServerFileID: file.ServerFileID, RelativePath: file.FileName}
	if err := s.db.Create(&track).Error; err != nil {
		t.Fatal(err)
	}
}
func cursorTestPage(t *testing.T, s *Service, device, cursor string) metadataCursorReply {
	t.Helper()
	page, err := s.metadataCursorBatch(device, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestMetadataCursorPaginationReplayAndReconnect(t *testing.T) {
	s := newTransferTestService(t)
	for n := 1; n <= 70; n++ {
		cursorTestSong(t, s, n, "phone")
	}
	first := cursorTestPage(t, s, "phone", "")
	if len(first.Items) != 32 || !first.HasMore {
		t.Fatalf("unexpected first page: %+v", first)
	}
	replay := cursorTestPage(t, s, "phone", "")
	if replay.Cursor != first.Cursor || len(replay.Items) != 32 {
		t.Fatal("an unacknowledged page must replay")
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page := cursorTestPage(t, s, "phone", cursor)
		for _, item := range page.Items {
			if seen[item.ServerFileID] {
				t.Fatal("duplicate paginated file")
			}
			seen[item.ServerFileID] = true
		}
		cursor = page.Cursor
		if !page.HasMore {
			break
		}
	}
	if len(seen) != 70 {
		t.Fatalf("got %d songs", len(seen))
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	next := cursorTestPage(t, s, "phone", cursor)
	if len(next.Items) != 0 || next.Cursor != cursor {
		t.Fatal("unchanged restart must not resend metadata")
	}
}

func TestMetadataCursorChangesCommitAtomically(t *testing.T) {
	s := newTransferTestService(t)
	file := cursorTestSong(t, s, 1, "phone")
	initial := cursorTestPage(t, s, "phone", "")
	abort := errors.New("rollback")
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&file).Update("title", "rolled back").Error; err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	rolledBack := cursorTestPage(t, s, "phone", initial.Cursor)
	if len(rolledBack.Items) != 0 || rolledBack.Cursor != initial.Cursor {
		t.Fatal("rollback leaked a change")
	}
	if err := s.db.Model(&file).Update("title", "offline edit").Error; err != nil {
		t.Fatal(err)
	}
	changed := cursorTestPage(t, s, "phone", initial.Cursor)
	if len(changed.Items) != 1 || changed.Items[0].Title != "offline edit" {
		t.Fatal("offline edit missing")
	}
	if err := s.db.Model(&file).Update("title", "offline edit").Error; err != nil {
		t.Fatal(err)
	}
	if page := cursorTestPage(t, s, "phone", changed.Cursor); len(page.Items) != 0 {
		t.Fatal("unchanged write produced metadata")
	}
}

func TestMetadataCursorNewAssociationAndDeviceScope(t *testing.T) {
	s := newTransferTestService(t)
	file := cursorTestSong(t, s, 1, "other-phone")
	before := cursorTestPage(t, s, "phone", "")
	if len(before.Items) != 0 {
		t.Fatal("another device's catalog leaked")
	}
	cursorTestAttach(t, s, file, "phone")
	after := cursorTestPage(t, s, "phone", before.Cursor)
	if len(after.Items) != 1 {
		t.Fatal("new association missed older metadata")
	}
	if err := s.enqueueDeviceMetadata("phone", []ServerFile{file}); err != nil {
		t.Fatal(err)
	}
	if page := cursorTestPage(t, s, "phone", after.Cursor); len(page.Items) != 1 {
		t.Fatal("explicit refresh missing")
	}
}

func TestMetadataCursorResourcesAndLargePayloads(t *testing.T) {
	s := newTransferTestService(t)
	file := cursorTestSong(t, s, 1, "phone")
	initial := cursorTestPage(t, s, "phone", "")
	resource := MetadataResource{ResourceID: "mr_cover", ServerFileID: file.ServerFileID, Type: "cover", ResourceHash: "cover-hash", StoragePath: "missing-cover-file", MIME: "image/png"}
	if err := s.db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	page := cursorTestPage(t, s, "phone", initial.Cursor)
	if len(page.Items) != 1 || len(page.Items[0].Resources) != 1 {
		t.Fatal("resource-only edit missing")
	}
	cover := page.Items[0].Resources[0]
	if cover.Data != "" || cover.URL != "/api/station/resources/mr_cover" {
		t.Fatal("artwork should be referenced, not embedded")
	}
	if err := s.db.Model(&resource).Update("deleted", true).Error; err != nil {
		t.Fatal(err)
	}
	deleted := cursorTestPage(t, s, "phone", page.Cursor)
	if len(deleted.Items) != 1 || !deleted.Items[0].Resources[0].Deleted {
		t.Fatal("resource tombstone missing")
	}
	if err := s.db.Model(&file).Update("lyrics", strings.Repeat("x", metadataCursorBatchBytes+100)).Error; err != nil {
		t.Fatal(err)
	}
	large := cursorTestPage(t, s, "phone", deleted.Cursor)
	if len(large.Items) != 1 || large.Items[0].ServerFileView != nil || large.Items[0].MetadataURL == "" {
		t.Fatal("oversized song should use HTTP")
	}
}

func TestMetadataCursorInvalidEpochFallsBackToFullSync(t *testing.T) {
	s := newTransferTestService(t)
	cursorTestSong(t, s, 1, "phone")
	page := cursorTestPage(t, s, "phone", "old-database:123")
	if !page.Reset || len(page.Items) != 1 {
		t.Fatal("invalid cursor must bootstrap again")
	}
}
