package station

import (
	"fmt"

	"gorm.io/gorm"
)

// Only the synchronization journal has a cursor. Audio identities and metadata
// models gain no version/timestamp fields. SQLite serializes writes, including
// these triggers, so a committed cursor cannot jump over an uncommitted edit.
func (s *Service) migrateMetadataCursor() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS metadata_sync_state (id INTEGER PRIMARY KEY, epoch TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS metadata_changes (cursor INTEGER PRIMARY KEY AUTOINCREMENT, server_file_id TEXT NOT NULL, device_id TEXT NOT NULL DEFAULT '')`,
			`CREATE INDEX IF NOT EXISTS idx_metadata_changes_file ON metadata_changes(server_file_id, cursor)`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		var count int64
		if err := tx.Table("metadata_sync_state").Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := tx.Exec(`INSERT INTO metadata_sync_state(id, epoch) VALUES (1, ?)`, newID("mc")).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO metadata_changes(server_file_id) SELECT server_file_id FROM server_files ORDER BY id`).Error; err != nil {
				return err
			}
		}
		// Watch semantic changes only: transfer progress, scans and unchanged
		// snapshots must not turn a reconnect into another full metadata sync.
		tables := []struct {
			name   string
			fields []string
		}{
			{"server_files", []string{"title", "artist", "album", "album_artist", "lyrics", "track_number", "disc_number", "deleted_at"}},
			{"metadata_resources", []string{"resource_hash", "data", "source", "format", "language", "deleted", "last_use_at", "storage_path", "mime"}},
		}
		for _, table := range tables {
			body := `INSERT INTO metadata_changes(server_file_id) VALUES (NEW.server_file_id);`
			if err := tx.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS metadata_cursor_%s_insert AFTER INSERT ON %s BEGIN %s END`, table.name, table.name, body)).Error; err != nil {
				return err
			}
			condition := ""
			for _, field := range table.fields {
				if condition != "" {
					condition += " OR "
				}
				condition += "OLD." + field + " IS NOT NEW." + field
			}
			if err := tx.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS metadata_cursor_%s_update AFTER UPDATE ON %s WHEN %s BEGIN %s END`, table.name, table.name, condition, body)).Error; err != nil {
				return err
			}
			if err := tx.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS metadata_cursor_%s_delete AFTER DELETE ON %s BEGIN INSERT INTO metadata_changes(server_file_id) VALUES (OLD.server_file_id); END`, table.name, table.name)).Error; err != nil {
				return err
			}
		}
		// A newly associated phone must receive metadata even if its global cursor
		// already passed that song's original metadata change.
		for _, statement := range []string{
			`CREATE TRIGGER IF NOT EXISTS metadata_cursor_track_insert AFTER INSERT ON tracks WHEN NEW.server_file_id <> '' BEGIN INSERT INTO metadata_changes(server_file_id,device_id) VALUES (NEW.server_file_id,NEW.device_id); END`,
			`CREATE TRIGGER IF NOT EXISTS metadata_cursor_track_update AFTER UPDATE ON tracks WHEN NEW.server_file_id <> '' AND (OLD.server_file_id IS NOT NEW.server_file_id OR OLD.device_id IS NOT NEW.device_id) BEGIN INSERT INTO metadata_changes(server_file_id,device_id) VALUES (NEW.server_file_id,NEW.device_id); END`,
		} {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
