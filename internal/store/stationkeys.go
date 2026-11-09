package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// keysDone is the value of PRAGMA user_version once every record has been given its station key.
const keysDone = 4

// BackfillStationKeys gives the records of a database made before station keys existed the key of their station: the place a
// record holds is turned into a key with keyOf, which is also given the station's label and the channel so that it can tell when
// the table is no longer in the order the records were made with (and then finds the station by its label). The records, the copies kept for Undo and the candidates
// are all brought along. A copy of the database is saved first. It does nothing once it has been done, and returns the number
// of records changed.
func (s *Store) BackfillStationKeys(keyOf func(id int, label string, channel int) string) (int, error) {
	var ver int
	if err := s.wdb.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		return 0, err
	}
	if ver >= keysDone {
		return 0, nil
	}
	var total int
	if err := s.wdb.QueryRow(`SELECT count(*) FROM bodies`).Scan(&total); err != nil {
		return 0, err
	}
	if total > 0 {
		if _, err := s.backup("before-station-keys", 3); err != nil {
			return 0, fmt.Errorf("could not save a backup before giving the stations their keys: %w", err)
		}
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// (a record that has no table row any more keeps its key empty)
	fix := func(data string) (string, string, bool) {
		var r Record
		if json.Unmarshal([]byte(data), &r) != nil {
			return "", "", false
		}
		changed := false
		if r.StationKey == "" && r.StationID > 0 {
			if k := keyOf(r.StationID, r.Station, r.Channel); k != "" {
				r.StationKey, changed = k, true
			}
		}
		if r.PickedKey == "" && r.Picked > 0 {
			if k := keyOf(r.Picked, "", r.Channel); k != "" {
				r.PickedKey, changed = k, true
			}
		}
		for i := range r.Candidates {
			if r.Candidates[i].Key == "" && r.Candidates[i].StationID > 0 {
				if k := keyOf(r.Candidates[i].StationID, r.Candidates[i].Label, r.Channel); k != "" {
					r.Candidates[i].Key, changed = k, true
				}
			}
		}
		if !changed {
			return "", "", false
		}
		b, err := json.Marshal(&r)
		if err != nil {
			return "", "", false
		}
		return string(b), r.StationKey, true
	}
	type edit struct {
		id        int64
		data, key string
	}
	collect := func(q string) ([]edit, error) {
		rows, err := tx.Query(q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []edit
		for rows.Next() {
			var id int64
			var data string
			if err := rows.Scan(&id, &data); err != nil {
				return nil, err
			}
			if nd, key, ok := fix(data); ok {
				out = append(out, edit{id, nd, key})
			}
		}
		return out, rows.Err()
	}
	bodies, err := collect(`SELECT id, data FROM bodies`)
	if err != nil {
		return 0, err
	}
	for _, e := range bodies {
		if _, err := tx.Exec(`UPDATE bodies SET data=? WHERE id=?`, e.data, e.id); err != nil {
			return 0, err
		}
		if e.key != "" {
			if _, err := tx.Exec(`UPDATE records SET station_key=? WHERE id=?`, e.key, e.id); err != nil {
				return 0, err
			}
		}
	}
	hist, err := collect(`SELECT rowid, data FROM edit_history`)
	if err != nil {
		return 0, err
	}
	for _, e := range hist {
		if _, err := tx.Exec(`UPDATE edit_history SET data=? WHERE rowid=?`, e.data, e.id); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, keysDone)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(bodies), nil
}

// ResyncStations brings the station_id column in line with the station table as it is now: idOf gives the place of a station
// key (ok false: the station is not in the table, and its records get 0, "no station"). It returns the number of records
// changed. Call it whenever the table has been replaced, reordered or extended.
func (s *Store) ResyncStations(idOf func(key string) (int, bool)) (int, error) {
	rows, err := s.wdb.Query(`SELECT DISTINCT station_key, station_id FROM records WHERE station_key != ''`)
	if err != nil {
		return 0, err
	}
	type pair struct {
		key      string
		have, to int
	}
	var todo []pair
	for rows.Next() {
		var k string
		var have int
		if err := rows.Scan(&k, &have); err != nil {
			rows.Close()
			return 0, err
		}
		to, _ := idOf(k)
		if to != have {
			todo = append(todo, pair{k, have, to})
		}
	}
	rows.Close()
	if len(todo) == 0 {
		return 0, nil
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, p := range todo {
		res, err := tx.Exec(`UPDATE records SET station_id=? WHERE station_key=? AND station_id=?`, p.to, p.key, p.have)
		if err != nil {
			return 0, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, tx.Commit()
}

// CountUnresolved returns how many records in the log name a station (by key) that is not in the station table now.
func (s *Store) CountUnresolved() int {
	var n int
	if err := s.rdb.QueryRow(`SELECT count(*) FROM records WHERE deleted_at IS NULL AND station_key != '' AND station_id = 0`).Scan(&n); err != nil && err != sql.ErrNoRows {
		return 0
	}
	return n
}
