package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Store is an open store.
type Store struct {
	mu  sync.RWMutex // guards the system log and the quality history
	dir string

	wdb *sql.DB // the one connection that writes; a transaction on it is a write lock
	rdb *sql.DB // connections for queries
	bdb *sql.DB // one connection for backups, so that they never wait for (or delay) a query

	live      atomic.Int64                                 // number of records in the log
	resolver  atomic.Pointer[func(key string) (int, bool)] // station key -> place in the station table (set by the catalog)
	lastPurge atomic.Int64                                 // unix time of the last emptying of the old trash

	logF     *os.File
	logLines int
	sys      []SyslogEntry

	qualF   *os.File
	qual    []QPoint
	qMinute int64
	qSum    float64
	qN      int
}

// tsFmt is how times are written in the database: ISO 8601, UTC, milliseconds. Text in this form sorts like the time.
const tsFmt = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(tsFmt)
}

// tsCeil is ts of the first millisecond that is not before t (stored times are whole milliseconds).
func tsCeil(t time.Time) string {
	u := t.UTC()
	if tr := u.Truncate(time.Millisecond); !tr.Equal(u) {
		u = tr.Add(time.Millisecond)
	}
	return ts(u)
}

func parseTS(s string) time.Time {
	t, err := time.Parse(tsFmt, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// dsn builds the connection string: the file name followed by options of the driver.
func dsn(path string, writer bool) string {
	q := "?_busy_timeout=15000&_journal_mode=WAL&_synchronous=NORMAL&_auto_vacuum=INCREMENTAL"
	if writer {
		q += "&_txlock=immediate"
	}
	return path + q
}

func openDB(path string, conns int, writer bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, writer))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxLifetime(0)
	return db, nil
}

// Open opens (creating if needed) the store in dir. A message file of an earlier version (messages.jsonl) is converted the first
// time; it is renamed, not deleted.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, dbFile)
	s := &Store{dir: dir, qMinute: -1}
	var err error
	if s.wdb, err = openDB(path, 1, true); err != nil {
		return nil, err
	}
	fail := func(err error) (*Store, error) {
		s.closeDBs()
		return nil, err
	}
	if err := s.initSchema(); err != nil {
		return fail(fmt.Errorf("%s: %w", dbFile, err))
	}
	if s.rdb, err = openDB(path, 4, false); err != nil {
		return fail(err)
	}
	if s.bdb, err = openDB(path, 1, false); err != nil {
		return fail(err)
	}
	if err := s.checkIntegrity(); err != nil {
		return fail(err)
	}
	migrated, err := s.migrateLegacy()
	if err != nil {
		return fail(fmt.Errorf("converting the earlier message file: %w", err))
	}
	if err := s.purgeTrash(); err != nil {
		return fail(err)
	}
	var n int64
	if err := s.rdb.QueryRow(`SELECT count(*) FROM records WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		return fail(err)
	}
	s.live.Store(n)
	if err := s.loadSyslog(); err != nil {
		return fail(err)
	}
	if err := s.loadQuality(); err != nil {
		return fail(err)
	}
	if migrated != "" {
		s.Log("info", migrated)
	}
	return s, nil
}

func (s *Store) initSchema() error {
	var ver int
	if err := s.wdb.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		return err
	}
	if ver >= schemaVersion {
		return nil
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(tableSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(viewsSQL); err != nil {
		return err
	}
	if ver >= 1 && ver < 2 { // version 2: the carrier frequency is kept in Hz (it was kHz)
		for _, q := range []string{
			`UPDATE records SET channel = channel * 1000 WHERE channel > 0 AND channel < 100000`,
			`UPDATE bodies SET data = json_set(data, '$.ch', json_extract(data, '$.ch') * 1000) WHERE json_extract(data, '$.ch') BETWEEN 1 AND 99999`,
			`UPDATE edit_history SET data = json_set(data, '$.ch', json_extract(data, '$.ch') * 1000) WHERE json_extract(data, '$.ch') BETWEEN 1 AND 99999`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
	}
	if ver >= 1 && ver < 3 { // version 3: a record refers to its station by key (the column is filled in by BackfillStationKeys)
		var have int
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('records') WHERE name='station_key'`).Scan(&have); err != nil {
			return err
		}
		if have == 0 {
			if _, err := tx.Exec(`ALTER TABLE records ADD COLUMN station_key TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// integrityLimit is the size above which the check at start-up is skipped (it reads the whole file).
const integrityLimit = 512 << 20

func (s *Store) checkIntegrity() error {
	if fi, err := os.Stat(filepath.Join(s.dir, dbFile)); err == nil && fi.Size() > integrityLimit {
		return nil
	}
	var res string
	if err := s.rdb.QueryRow(`PRAGMA quick_check(1)`).Scan(&res); err != nil {
		return fmt.Errorf("%s cannot be read: %w", dbFile, err)
	}
	if res != "ok" {
		return fmt.Errorf("%s is damaged (%s); restore a copy from the backups folder", dbFile, res)
	}
	return nil
}

// Check runs the full integrity check of the database and returns what it found (nil when all is well).
func (s *Store) Check() error {
	rows, err := s.bdb.Query(`PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var r string
		if rows.Scan(&r) == nil && r != "ok" {
			bad = append(bad, r)
		}
	}
	if len(bad) > 0 {
		if len(bad) > 5 {
			bad = bad[:5]
		}
		return errors.New("database check: " + strings.Join(bad, "; "))
	}
	return rows.Err()
}

func (s *Store) closeDBs() {
	for _, db := range []*sql.DB{s.wdb, s.rdb, s.bdb} {
		if db != nil {
			db.Close()
		}
	}
}

// normalize rounds times to the millisecond and qualities to two decimals, as they are stored.
func normalize(r *Record) {
	legacyKHz(r)
	r.Received, r.End = r.Received.Round(time.Millisecond), r.End.Round(time.Millisecond)
	r.Quality = F2(math.Round(float64(r.Quality)*100) / 100)
	r.IdentScore = F2(math.Round(float64(r.IdentScore)*100) / 100)
	if len(r.Lines) > 0 {
		ls := make([]Line, len(r.Lines))
		for i, l := range r.Lines {
			l.Time = l.Time.Round(time.Millisecond)
			l.Quality = F2(math.Round(float64(l.Quality)*100) / 100)
			ls[i] = l
		}
		r.Lines = ls
	}
}

// --- writing records ---

const recCols = `received, channel, kind, b1, b2, serial, navarea, station, distance_km, bearing_deg, quality, comment,
 end_time, rcv, station_id, ident_method, ident_score, manual, picked, version, station_key`

const recColCount = 21

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// recArgs lists the values of recCols for r, and those of the body (text, corrected, data).
func recArgs(r *Record) (rec, body []any, err error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, nil, err
	}
	return []any{ts(r.Received), r.Channel, r.Kind, r.B1, r.B2, r.Serial, r.NavArea, r.Station, r.DistanceKm, r.BearingDeg,
			float64(r.Quality), r.Comment, ts(r.End), r.Rcv, r.StationID, r.IdentMethod, float64(r.IdentScore),
			b2i(r.Manual), r.Picked, r.Version, r.StationKey},
		[]any{r.Text(), r.Corrected, string(data)}, nil
}

var insertSQL = `INSERT INTO records(id, ` + recCols + `) VALUES (?` + strings.Repeat(`,?`, recColCount) + `)`

// updateSQL replaces all columns of a record and takes it out of the trash if it was there.
var updateSQL = func() string {
	cols := strings.Split(strings.NewReplacer("\n", "", " ", "").Replace(recCols), ",")
	for i, c := range cols {
		cols[i] = c + "=?"
	}
	return `UPDATE records SET ` + strings.Join(cols, ",") + `, deleted_at=NULL, batch_id=NULL WHERE id=?`
}()

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func insertRec(tx execer, r *Record) error {
	legacyKHz(r)
	a, body, err := recArgs(r)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(insertSQL, append([]any{r.ID}, a...)...); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO bodies(id, text, corrected, data) VALUES (?, ?, ?, ?)`, append([]any{r.ID}, body...)...)
	return err
}

func updateRec(tx execer, r *Record) error {
	legacyKHz(r)
	a, body, err := recArgs(r)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(updateSQL, append(a, r.ID)...); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO bodies(id, text, corrected, data) VALUES (?, ?, ?, ?)`, append([]any{r.ID}, body...)...)
	return err
}

// nextID is the number a new record gets: one more than any number ever used.
func nextID(tx execer) (int64, error) {
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='records'), 0)`).Scan(&seq); err != nil {
		return 0, err
	}
	return seq + 1, nil
}

// Add stores a new record (its ID and Version are assigned) and returns it.
func (s *Store) Add(r Record) (Record, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	if r.ID, err = nextID(tx); err != nil {
		return Record{}, err
	}
	r.Version = 1
	normalize(&r)
	if r.Station == "" {
		r.Station = "UNID"
	}
	if err := insertRec(tx, &r); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	s.live.Add(1)
	return r, nil
}

func decode(data string) (Record, error) {
	var r Record
	err := json.Unmarshal([]byte(data), &r)
	legacyKHz(&r)
	return r, err
}

// decode reads a record back and gives its station, its picked station and its candidates the place they have in the station
// table now: a record refers to a station by its key, and the table may have been reordered, extended or cut since.
func (s *Store) decode(data string) (Record, error) {
	r, err := decode(data)
	if err == nil {
		s.placeStations(&r)
	}
	return r, err
}

// SetResolver tells the store how to turn a station key into the station's place in the table (ok false: no such station).
func (s *Store) SetResolver(f func(key string) (int, bool)) { s.resolver.Store(&f) }

func (s *Store) placeStations(r *Record) {
	p := s.resolver.Load()
	if p == nil {
		return
	}
	f := *p
	if r.StationKey != "" {
		r.StationID, _ = f(r.StationKey) // (0 when the station is no longer in the table)
	}
	if r.PickedKey == PickedNone {
		r.Picked = -1
	} else if r.PickedKey != "" {
		r.Picked, _ = f(r.PickedKey)
	}
	if len(r.Candidates) > 0 {
		keep := r.Candidates[:0:0]
		for _, c := range r.Candidates {
			if c.Key != "" {
				id, ok := f(c.Key)
				if !ok {
					continue
				}
				c.StationID = id
			}
			keep = append(keep, c)
		}
		r.Candidates = keep
	}
}

// Get returns a copy of a record.
func (s *Store) Get(id int64) (Record, bool) {
	var data string
	if err := s.rdb.QueryRow(`SELECT b.data FROM records r JOIN bodies b ON b.id = r.id WHERE r.id=? AND r.deleted_at IS NULL`, id).Scan(&data); err != nil {
		return Record{}, false
	}
	r, err := s.decode(data)
	return r, err == nil
}

// Update replaces a record. r.Version must equal the stored version (ErrConflict otherwise); the new version is returned.
func (s *Store) Update(r Record) (Record, error) { return s.update(r, "") }

// UpdateUndoable is Update for an edit by the operator: the record as it was is saved first, so that Undo can bring it back
// (desc says what was changed).
func (s *Store) UpdateUndoable(r Record, desc string) (Record, error) { return s.update(r, desc) }

func (s *Store) update(r Record, desc string) (Record, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	var ver int
	var old string
	err = tx.QueryRow(`SELECT r.version, b.data FROM records r JOIN bodies b ON b.id = r.id WHERE r.id=? AND r.deleted_at IS NULL`, r.ID).Scan(&ver, &old)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	if ver != r.Version {
		return Record{}, ErrConflict
	}
	if desc != "" {
		res, err := tx.Exec(`INSERT INTO batches(at, kind, descr) VALUES (?, 'edit', ?)`, ts(time.Now()), desc)
		if err != nil {
			return Record{}, err
		}
		bid, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO edit_history(batch_id, rec_id, data) VALUES (?, ?, ?)`, bid, r.ID, old); err != nil {
			return Record{}, err
		}
	}
	r.Version++
	normalize(&r)
	if err := updateRec(tx, &r); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return r, nil
}

// --- deleting ---

const idChunk = 500

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// Delete moves records to the trash (Undo or Restore brings them back); it returns how many were in the log.
func (s *Store) Delete(ids ...int64) (int, error) {
	return s.trashWhere(func(tx execer, now string, bid int64) (int, error) {
		n := 0
		for i := 0; i < len(ids); i += idChunk {
			part := ids[i:min(i+idChunk, len(ids))]
			args := []any{now, bid}
			for _, id := range part {
				args = append(args, id)
			}
			res, err := tx.Exec(`UPDATE records SET deleted_at=?, batch_id=? WHERE deleted_at IS NULL AND id IN (`+placeholders(len(part))+`)`, args...)
			if err != nil {
				return n, err
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		return n, nil
	})
}

// DeleteWhere moves every record matching f (ignoring Limit, Offset and Newest) to the trash and returns the count.
func (s *Store) DeleteWhere(f Filter) (int, error) {
	where, args := f.where(false)
	return s.trashWhere(func(tx execer, now string, bid int64) (int, error) {
		res, err := tx.Exec(`UPDATE records SET deleted_at=?, batch_id=? WHERE id IN (SELECT r.id FROM `+f.from()+` WHERE `+where+`)`,
			append([]any{now, bid}, args...)...)
		if err != nil {
			return 0, err
		}
		k, _ := res.RowsAffected()
		return int(k), nil
	})
}

// EraseWhere deletes every record in the log matching f for good (ignoring Limit, Offset and Newest): nothing goes to the trash and
// Undo cannot bring them back. It returns the count. (Compact then gives the space back.)
func (s *Store) EraseWhere(f Filter) (int, error) {
	where, args := f.where(false)
	sel := `SELECT r.id FROM ` + f.from() + ` WHERE ` + where
	tx, err := s.wdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM edit_history WHERE rec_id IN (` + sel + `)`,
		`DELETE FROM bodies WHERE id IN (` + sel + `)`,
	} {
		if _, err := tx.Exec(q, args...); err != nil {
			return 0, err
		}
	}
	res, err := tx.Exec(`DELETE FROM records WHERE id IN (`+sel+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, nil
	}
	if err := dropEmptyBatches(tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.live.Add(-n)
	return int(n), nil
}

// trashWhere runs do inside one transaction with a new deletion batch; nothing is written when do deletes nothing.
func (s *Store) trashWhere(do func(tx execer, now string, bid int64) (int, error)) (int, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := ts(time.Now())
	res, err := tx.Exec(`INSERT INTO batches(at, kind) VALUES (?, 'delete')`, now)
	if err != nil {
		return 0, err
	}
	bid, _ := res.LastInsertId()
	n, err := do(tx, now, bid)
	if err != nil || n == 0 {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.live.Add(int64(-n))
	s.maybePurge()
	return n, nil
}

// --- queries ---

// Count returns the number of records in the log.
func (s *Store) Count() int { return int(s.live.Load()) }

func likePattern(q string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}

// where is the SQL condition for f (the rows in the log, or in the trash); the order, Limit and Offset are not part of it.
// It selects what Filter.match does (Text: ASCII letters are matched without regard to case).
func (f Filter) where(trash bool) (string, []any) {
	w := []string{"deleted_at IS NULL"}
	if trash {
		w[0] = "deleted_at IS NOT NULL"
	}
	var a []any
	add := func(cond string, args ...any) {
		w = append(w, cond)
		a = append(a, args...)
	}
	if f.Channel != 0 {
		add("channel = ?", f.Channel)
	}
	if f.Channels != nil {
		var in []any
		for k, v := range f.Channels {
			if v {
				in = append(in, k)
			}
		}
		if len(in) == 0 {
			add("0")
		} else {
			add("channel IN (?"+strings.Repeat(",?", len(in)-1)+")", in...)
		}
	}
	if f.Rcv != 0 {
		add("rcv = ?", f.Rcv-1)
	}
	if f.Rcvs != nil {
		var in []any
		for k, v := range f.Rcvs {
			if v {
				in = append(in, k-1)
			}
		}
		if len(in) == 0 {
			add("0")
		} else {
			add("rcv IN (?"+strings.Repeat(",?", len(in)-1)+")", in...)
		}
	}
	if f.Kinds != nil {
		var in []any
		for k, v := range f.Kinds {
			if v {
				in = append(in, k)
			}
		}
		if len(in) == 0 {
			add("0")
		} else {
			add("kind IN (?"+strings.Repeat(",?", len(in)-1)+")", in...)
		}
	}
	if f.NavArea != 0 {
		add("navarea = ?", f.NavArea)
	}
	if f.B1 != "" && f.B1 != "*" {
		add("b1 = ?", f.B1)
	}
	if f.NavAreas != nil {
		var in []any
		for k, v := range f.NavAreas {
			if v {
				in = append(in, k)
			}
		}
		if len(in) == 0 {
			add("0")
		} else {
			add("navarea IN ("+placeholders(len(in))+")", in...)
		}
	}
	if f.B1s != nil {
		var in []any
		for k, v := range f.B1s {
			if v {
				in = append(in, k)
			}
		}
		if len(in) == 0 {
			add("0")
		} else {
			add("b1 IN ("+placeholders(len(in))+")", in...)
		}
	}
	if f.OmitUnid {
		add("station_id != 0")
	}
	if !f.From.IsZero() {
		add("received >= ?", tsCeil(f.From))
	}
	if !f.To.IsZero() {
		add("received < ?", tsCeil(f.To))
	}
	if f.Kind != "" {
		add("kind = ?", f.Kind)
	}
	if f.Unidentified {
		add("station_id = 0")
	}
	if f.MaxDistance > 0 {
		add("(station_id != 0 AND distance_km < ?)", f.MaxDistance)
	}
	if f.MinDistance > 0 {
		add("(station_id != 0 AND distance_km >= ?)", f.MinDistance)
	}
	if f.Text != "" {
		p := likePattern(f.Text)
		add(`(text LIKE ? ESCAPE '\' OR corrected LIKE ? ESCAPE '\')`, p, p)
	}
	return strings.Join(w, " AND "), a
}

// from is the table (with the texts when f searches them); records are called r, texts b.
func (f Filter) from() string {
	if f.Text != "" {
		return "records r JOIN bodies b ON b.id = r.id"
	}
	return "records r"
}

func (f Filter) order() string {
	if f.Newest {
		return " ORDER BY r.received DESC, r.id DESC"
	}
	return " ORDER BY r.received, r.id"
}

func (f Filter) paging() (string, []any) {
	switch {
	case f.Limit > 0:
		return " LIMIT ? OFFSET ?", []any{f.Limit, max(f.Offset, 0)}
	case f.Offset > 0:
		return " LIMIT -1 OFFSET ?", []any{f.Offset}
	}
	return "", nil
}

// Query returns the matching records (copies) honouring Limit, Offset and Newest, and the total number of matches.
func (s *Store) Query(f Filter) ([]Record, int) {
	rows, total := s.queryRows(f, false)
	out := make([]Record, len(rows))
	for i, r := range rows {
		out[i] = r.Record
	}
	return out, total
}

func (s *Store) queryRows(f Filter, trash bool) ([]TrashedRecord, int) {
	where, args := f.where(trash)
	var total int
	if !trash && len(args) == 0 && where == "deleted_at IS NULL" {
		total = s.Count() // no condition: the whole log
	} else if err := s.rdb.QueryRow(`SELECT count(*) FROM `+f.from()+` WHERE `+where, args...).Scan(&total); err != nil {
		s.dbError("count", err)
		return nil, 0
	}
	pg, pa := f.paging()
	order := f.order()
	if trash {
		order = " ORDER BY r.deleted_at DESC, r.id DESC" // what was deleted last first
	}
	rs, err := s.rdb.Query(`SELECT b.data, COALESCE(r.deleted_at, ''), COALESCE(r.batch_id, 0) FROM records r JOIN bodies b ON b.id = r.id WHERE `+where+order+pg,
		append(append([]any(nil), args...), pa...)...)
	if err != nil {
		s.dbError("query", err)
		return nil, 0
	}
	defer rs.Close()
	out := []TrashedRecord{}
	for rs.Next() {
		var data, del string
		var batch int64
		if err := rs.Scan(&data, &del, &batch); err != nil {
			s.dbError("query", err)
			break
		}
		r, err := s.decode(data)
		if err != nil {
			continue
		}
		out = append(out, TrashedRecord{Record: r, DeletedAt: parseTS(del), Batch: batch})
	}
	return out, total
}

// dbError reports a failed query in the system log (a query has no other way to say so).
func (s *Store) dbError(what string, err error) {
	s.Log("error", fmt.Sprintf("message database: %s failed: %v", what, err))
}

// Locate returns the position of record id in the list f selects (ordered as Query would, Limit and Offset ignored), or -1
// when it is not in it.
func (s *Store) Locate(f Filter, id int64) int {
	where, args := f.where(false)
	var rx string
	if err := s.rdb.QueryRow(`SELECT r.received FROM `+f.from()+` WHERE r.id=? AND `+where, append([]any{id}, args...)...).Scan(&rx); err != nil {
		return -1
	}
	op := "<"
	if f.Newest {
		op = ">"
	}
	var n int
	if err := s.rdb.QueryRow(`SELECT count(*) FROM `+f.from()+` WHERE `+where+` AND (r.received, r.id) `+op+` (?, ?)`,
		append(append([]any(nil), args...), rx, id)...).Scan(&n); err != nil {
		return -1
	}
	return n
}

// CountByStation returns how many records each station ID (non-zero) has.
func (s *Store) CountByStation() map[int]int {
	m := map[int]int{}
	rs, err := s.rdb.Query(`SELECT station_id, count(*) FROM records WHERE deleted_at IS NULL AND station_id != 0 GROUP BY station_id`)
	if err != nil {
		s.dbError("count by station", err)
		return m
	}
	defer rs.Close()
	for rs.Next() {
		var id, n int
		if rs.Scan(&id, &n) == nil {
			m[id] = n
		}
	}
	return m
}

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

// Close flushes and closes the files and the database.
func (s *Store) Close() error {
	s.mu.Lock()
	s.flushQualityLocked()
	var first error
	for _, f := range []*os.File{s.logF, s.qualF} {
		if f != nil {
			if err := f.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	s.mu.Unlock()
	var a, b, c int
	s.wdb.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&a, &b, &c)
	s.closeDBs()
	return first
}
