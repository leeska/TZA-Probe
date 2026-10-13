// Package storage is the SQLite adapter. It never imports HTTP, Agent, or UI code.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/leeska/TZA-Probe/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = model.ErrNotFound

var ErrConflict = model.ErrConflict

type Store struct {
	path   string // the database file; Backup writes its snapshot next to it
	db     *sql.DB
	read   *sql.DB
	writes sync.Mutex
	// 后台分批删除（见 purge.go）。
	purgeWake chan struct{}
	purgeStop context.CancelFunc
	purgeDone chan struct{}
	// tcpBucketsFrom 是网络历史格子（见 tcp_buckets.go）开始完整的时刻：0 表示全部回填完，
	// 否则只有不早于它的时间段能从格子里读。
	tcpBucketsFrom atomic.Int64
}

// readConns is how many connections serve read-only queries. One writer
// connection is enough for the write paths (they are serialized by writes), but
// the panel fans out: the home page asks for the node list, the last probes of
// every node and the per-target observations at once, and with a single
// connection those 18 requests queue head to tail. 线上就是这样：延迟与回程要
// 等好几秒才出来，而每次请求本身只要几十毫秒。
const readConns = 4

// openReadOnly opens the same file a second time for reads. WAL lets readers and
// the writer run side by side, and query_only makes a stray write fail loudly
// instead of quietly going through a connection that never sets foreign_keys.
// The PRAGMAs ride in the DSN because a plain Exec only reaches the connections
// that already exist, and a pool creates more later.
func openReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(readConns)
	db.SetMaxIdleConns(readConns)
	return db, nil
}

// query / queryRow are the read paths: they go to the read pool so a slow panel
// request cannot hold up the writer or a second panel request.
func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.read.QueryContext(ctx, query, args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.read.QueryRowContext(ctx, query, args...)
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	// foreign_keys and busy_timeout are per connection: in the DSN they reach
	// every connection the pool opens, including one that replaces a broken
	// writer. A one-off Exec would leave that replacement without cascades.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, db: db}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	read, err := openReadOnly(path)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.read = read
	var next, liveFrom int64
	if err = db.QueryRow("SELECT next,live_from FROM tcp_buckets_state WHERE id=1").Scan(&next, &liveFrom); err != nil {
		s.Close()
		return nil, err
	}
	if next < liveFrom {
		s.tcpBucketsFrom.Store(liveFrom)
	}
	s.startPurger()
	return s, nil
}

func (s *Store) Close() error {
	// 正在删的那一批做完就停；没删完的留在 purges 里，下次 Open 接着删。
	s.stopPurger()
	err := s.db.Close()
	if s.read != nil {
		if rerr := s.read.Close(); err == nil {
			err = rerr
		}
	}
	return err
}

func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
