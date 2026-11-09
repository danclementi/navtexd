package store

// schemaVersion is stored in the database (PRAGMA user_version). When it is raised, the views are dropped and created again.
const schemaVersion = 3

// The records table has a readable column for every field a person might want to look at, so that a viewer (the sqlite3 command
// line, DB Browser for SQLite and the like) shows a normal table. The program itself reads the whole record back from the data
// column (JSON), so a new Record field never needs a change of schema. Times are ISO 8601 text in UTC (they sort correctly and
// read naturally). A deleted record is not removed: deleted_at and batch_id are set, and it stays in the table until the trash
// is emptied (TrashKeep).
const tableSQL = `
CREATE TABLE IF NOT EXISTS records (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,  -- numbers are never reused, not even after a record is purged
  received     TEXT    NOT NULL,                   -- UTC, e.g. 2026-11-02T20:50:12.345Z
  channel      INTEGER NOT NULL,                   -- carrier frequency in Hz (518000, 4209500)
  kind         TEXT    NOT NULL,                   -- message, incomplete, fragment
  b1           TEXT    NOT NULL,                   -- station identification letter
  b2           TEXT    NOT NULL,                   -- subject indicator letter
  serial       TEXT    NOT NULL,
  navarea      INTEGER NOT NULL,
  station      TEXT    NOT NULL,                   -- station label, UNID when unknown
  distance_km  REAL    NOT NULL,
  bearing_deg  REAL    NOT NULL,
  quality      REAL    NOT NULL,
  comment      TEXT    NOT NULL,
  end_time     TEXT    NOT NULL,
  rcv          INTEGER NOT NULL,                   -- decoding channel: 0 = the first, 1 = the second
  station_id   INTEGER NOT NULL,                   -- 0 = unknown; the station's place in the table (kept up to date when the table changes)
  ident_method TEXT    NOT NULL,
  ident_score  REAL    NOT NULL,
  manual       INTEGER NOT NULL,                   -- 1 = B1 / NAVAREA set by the operator
  picked       INTEGER NOT NULL,
  version      INTEGER NOT NULL,                   -- changes with every edit (detects a stale copy)
  deleted_at   TEXT,                               -- NULL = in the log; else when it was deleted (it is in the trash)
  batch_id     INTEGER,                            -- the deletion it belongs to
  station_key  TEXT    NOT NULL DEFAULT ''         -- the station's own identity (frequency, NAVAREA, B1, call sign): what a record really refers to
);
-- One covering index for the log: the list, its total and the filters read only this index, not the table. (Earlier databases
-- had rec_time and rec_chan, which this one replaces.)
DROP INDEX IF EXISTS rec_time;
DROP INDEX IF EXISTS rec_chan;
DROP INDEX IF EXISTS rec_station; -- (the planner preferred it for counts and then read the table row by row)
CREATE INDEX IF NOT EXISTS rec_filter ON records(received, id, channel, rcv, navarea, b1, kind, station_id, distance_km) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS rec_deleted ON records(deleted_at)        WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS rec_batch   ON records(batch_id)          WHERE batch_id IS NOT NULL;

-- The text of a record is kept apart from the table above, whose short rows are scanned by every query. The program reads the
-- whole record back from data (JSON: the lines with their times and qualities, candidates, revisions), so a new Record field
-- never needs a change of schema. Same id as the record.
CREATE TABLE IF NOT EXISTS bodies (
  id        INTEGER PRIMARY KEY,
  text      TEXT NOT NULL,                         -- the lines as received, joined with newlines
  corrected TEXT NOT NULL,                         -- the body after word autocorrection (may be empty)
  data      TEXT NOT NULL
);

-- one row for each action of the operator that Undo can take back: a deletion (its records have batch_id set) or an edit
CREATE TABLE IF NOT EXISTS batches (
  id    INTEGER PRIMARY KEY AUTOINCREMENT,
  at    TEXT NOT NULL,
  kind  TEXT NOT NULL,                             -- delete or edit
  descr TEXT NOT NULL DEFAULT ''
);

-- the record as it was before an edit
CREATE TABLE IF NOT EXISTS edit_history (
  batch_id INTEGER NOT NULL,
  rec_id   INTEGER NOT NULL,
  data     TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS edit_batch ON edit_history(batch_id);
`

// The views are for people who open the file with another program.
const viewsSQL = `
DROP VIEW IF EXISTS messages;
DROP VIEW IF EXISTS message_lines;
DROP VIEW IF EXISTS trash_messages;

-- the messages in the log, oldest first
CREATE VIEW messages AS
  SELECT r.id, r.received AS received_utc, r.channel / 1000.0 AS channel_khz, r.kind, r.b1, r.b2, r.serial, r.navarea, r.station,
         r.distance_km, r.bearing_deg, r.quality, r.comment, b.text, b.corrected AS corrected_text
  FROM records r JOIN bodies b ON b.id = r.id WHERE r.deleted_at IS NULL ORDER BY r.received, r.id;

-- every line of every message with its time and quality
CREATE VIEW message_lines AS
  SELECT r.id AS message_id, j.key + 1 AS line_no, json_extract(j.value, '$.t') AS time_utc,
         json_extract(j.value, '$.q') AS quality, json_extract(j.value, '$.x') AS text
  FROM records r JOIN bodies b ON b.id = r.id, json_each(b.data, '$.lines') AS j WHERE r.deleted_at IS NULL;

-- the messages in the trash (they can be brought back by the program for 30 days)
CREATE VIEW trash_messages AS
  SELECT r.id, r.deleted_at AS deleted_utc, r.received AS received_utc, r.channel / 1000.0 AS channel_khz, r.kind, r.b1, r.b2, r.serial,
         r.navarea, r.station, r.comment, b.text
  FROM records r JOIN bodies b ON b.id = r.id WHERE r.deleted_at IS NOT NULL ORDER BY r.deleted_at DESC, r.id;
`
