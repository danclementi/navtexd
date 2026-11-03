package store

import (
	"time"
)

// Compact empties the trash of what is too old, gives the free space in the database file back to the system and moves the
// journal into the main file. It runs in small steps while the program goes on reading and writing.
func (s *Store) Compact() error {
	if err := s.purgeTrash(); err != nil {
		return err
	}
	for i := 0; i < 100000; i++ {
		var free int
		if err := s.wdb.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
			return err
		}
		if free == 0 {
			break
		}
		if _, err := s.wdb.Exec(`PRAGMA incremental_vacuum(2000)`); err != nil {
			return err
		}
		time.Sleep(time.Millisecond) // lets a waiting write in between the steps
	}
	var busy, log, done int
	if err := s.wdb.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &done); err != nil {
		return err
	}
	_, err := s.wdb.Exec(`PRAGMA optimize`)
	return err
}
