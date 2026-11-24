package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// Store is a tiny document store on top of SQLite. Every entity kind is kept
// as JSON in the obj table and cached in memory, so reads never hit the disk.
type Store struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]map[int64][]byte
	dir   string
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "panel.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS obj(kind TEXT NOT NULL, id INTEGER NOT NULL, data TEXT NOT NULL, PRIMARY KEY(kind,id))`,
		`CREATE TABLE IF NOT EXISTS log(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, scope TEXT NOT NULL, ref INTEGER NOT NULL, level TEXT NOT NULL, msg TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS log_scope ON log(scope, ref, id)`,
		`CREATE TABLE IF NOT EXISTS metric(ts INTEGER NOT NULL, server INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS metric_ts ON metric(server, ts)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	s := &Store{db: db, cache: map[string]map[int64][]byte{}, dir: dir}
	rows, err := db.Query(`SELECT kind,id,data FROM obj`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var id int64
		var d []byte
		if err := rows.Scan(&k, &id, &d); err != nil {
			return nil, err
		}
		if s.cache[k] == nil {
			s.cache[k] = map[int64][]byte{}
		}
		s.cache[k][id] = d
	}
	return s, nil
}

func (s *Store) Dir() string { return s.dir }
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) NextID(kind string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var max int64
	for id := range s.cache[kind] {
		if id > max {
			max = id
		}
	}
	return max + 1
}

func (s *Store) Put(kind string, id int64, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`INSERT INTO obj(kind,id,data) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data`, kind, id, string(b)); err != nil {
		return err
	}
	if s.cache[kind] == nil {
		s.cache[kind] = map[int64][]byte{}
	}
	s.cache[kind][id] = b
	return nil
}

func (s *Store) Get(kind string, id int64, v any) error {
	s.mu.RLock()
	b, ok := s.cache[kind][id]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	return json.Unmarshal(b, v)
}

func (s *Store) Delete(kind string, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM obj WHERE kind=? AND id=?`, kind, id); err != nil {
		return err
	}
	delete(s.cache[kind], id)
	return nil
}

// IDs returns sorted ids of a kind.
func (s *Store) IDs(kind string) []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]int64, 0, len(s.cache[kind]))
	for id := range s.cache[kind] {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// List decodes every object of kind into a fresh T.
func List[T any](s *Store, kind string) []T {
	ids := s.IDs(kind)
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		var v T
		if s.Get(kind, id, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (s *Store) Log(scope string, ref int64, level, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec(`INSERT INTO log(ts,scope,ref,level,msg) VALUES(?,?,?,?,?)`, time.Now().UnixMilli(), scope, ref, level, msg)
}

type LogEntry struct {
	ID    int64  `json:"id"`
	TS    int64  `json:"ts"`
	Scope string `json:"scope"`
	Ref   int64  `json:"ref"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

func (s *Store) Logs(scope string, ref int64, limit int) []LogEntry {
	q := `SELECT id,ts,scope,ref,level,msg FROM log WHERE scope=? AND ref=? ORDER BY id DESC LIMIT ?`
	args := []any{scope, ref, limit}
	if ref < 0 {
		q = `SELECT id,ts,scope,ref,level,msg FROM log WHERE scope=? ORDER BY id DESC LIMIT ?`
		args = []any{scope, limit}
	}
	if scope == "" {
		q = `SELECT id,ts,scope,ref,level,msg FROM log ORDER BY id DESC LIMIT ?`
		args = []any{limit}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		rows.Scan(&e.ID, &e.TS, &e.Scope, &e.Ref, &e.Level, &e.Msg)
		out = append(out, e)
	}
	return out
}

func (s *Store) ClearLogs(scope string, ref int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec(`DELETE FROM log WHERE scope=? AND ref=?`, scope, ref)
}

func (s *Store) AddMetric(server int64, data any) {
	b, _ := json.Marshal(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec(`INSERT INTO metric(ts,server,data) VALUES(?,?,?)`, time.Now().Unix(), server, string(b))
}

func (s *Store) Metrics(server int64, since int64) []json.RawMessage {
	rows, err := s.db.Query(`SELECT data FROM metric WHERE server=? AND ts>=? ORDER BY ts`, server, since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var d string
		rows.Scan(&d)
		out = append(out, json.RawMessage(d))
	}
	return out
}

// Prune trims logs and metrics so the database stays small forever.
func (s *Store) Prune(logKeep int, metricDays int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec(`DELETE FROM log WHERE id <= (SELECT id FROM log ORDER BY id DESC LIMIT 1 OFFSET ?)`, logKeep)
	s.db.Exec(`DELETE FROM metric WHERE ts < ?`, time.Now().Add(-time.Duration(metricDays)*24*time.Hour).Unix())
}

// Backup writes a consistent copy of the database into dir/backup.
func (s *Store) Backup() (string, error) {
	bdir := filepath.Join(s.dir, "backup")
	os.MkdirAll(bdir, 0o755)
	name := filepath.Join(bdir, fmt.Sprintf("panel-%s.db", time.Now().Format("20060102-150405")))
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`VACUUM INTO ?`, name)
	return name, err
}

func (s *Store) Close() error { return s.db.Close() }
