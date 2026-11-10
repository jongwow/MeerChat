package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, keeps the binary CGO-free
)

// SQLite is a Store backed by a SQLite database file.
type SQLite struct {
	db      *sql.DB
	perRoom int
}

const schema = `
CREATE TABLE IF NOT EXISTS heads (
	route TEXT PRIMARY KEY,
	seq   INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS messages (
	route TEXT    NOT NULL,
	seq   INTEGER NOT NULL,
	peer  TEXT    NOT NULL,
	ts    INTEGER NOT NULL,
	ct    BLOB    NOT NULL,
	PRIMARY KEY (route, seq)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS messages_ts ON messages (ts);
`

// OpenSQLite opens (creating if needed) a SQLite store at path, keeping at
// most perRoom messages per room.
func OpenSQLite(path string, perRoom int) (*SQLite, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; serializing here avoids SQLITE_BUSY churn.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &SQLite{db: db, perRoom: max(perRoom, 0)}, nil
}

func (s *SQLite) Append(ctx context.Context, route, peer string, ts int64, ct []byte) (Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var seq uint64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO heads (route, seq) VALUES (?, 1)
		 ON CONFLICT (route) DO UPDATE SET seq = seq + 1
		 RETURNING seq`, route).Scan(&seq)
	if err != nil {
		return Message{}, err
	}
	if s.perRoom > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages (route, seq, peer, ts, ct) VALUES (?, ?, ?, ?, ?)`,
			route, seq, peer, ts, ct); err != nil {
			return Message{}, err
		}
		if seq > uint64(s.perRoom) {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM messages WHERE route = ? AND seq <= ?`,
				route, seq-uint64(s.perRoom)); err != nil {
				return Message{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return Message{Seq: seq, Peer: peer, TS: ts, CT: ct}, nil
}

func (s *SQLite) Since(ctx context.Context, route string, after uint64, limit int) ([]Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, peer, ts, ct FROM messages
		 WHERE route = ? AND seq > ? ORDER BY seq DESC LIMIT ?`,
		route, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Seq, &m.Peer, &m.TS, &m.CT); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	slices.Reverse(out)
	return out, rows.Err()
}

func (s *SQLite) Head(ctx context.Context, route string) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM heads WHERE route = ?`, route).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return seq, err
}

func (s *SQLite) Prune(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE ts < ?`, cutoff.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s *SQLite) Close() error { return s.db.Close() }
