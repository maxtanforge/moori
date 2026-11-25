// Package ledger records Agent execution ownership and reports on local disk.
package ledger

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

var (
	ErrConflict = errors.New("execution ID has different input")
	ErrBusy     = errors.New("target already has an active execution")
	ErrExpired  = errors.New("execution admission expired")
	ErrInvalid  = errors.New("invalid execution input")
	ErrState    = errors.New("invalid execution transition")
)

//go:embed schema.sql
var schema string

//go:embed documents.sql
var documents string

//go:embed retire.sql
var retire string

type Ledger struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Ledger, error) {
	db, err := sqlite.Open(ctx, path, 0x4d4f4f41, schema, documents, retire)
	if err != nil {
		return nil, err
	}
	return &Ledger{db: db}, nil
}

func (l *Ledger) Close() error { return l.db.Close() }

type Request struct {
	Execution, Target, Digest string
	Document                  string
	Expires                   time.Time
}

type Entry struct {
	Request
	State    deployment.State
	Sequence int64
}

func (l *Ledger) Get(ctx context.Context, id string) (Entry, error) {
	return readEntry(l.db.QueryRowContext(ctx, `SELECT id,target,digest,document,expires_ms,state,sequence
 FROM executions WHERE id=?`, id))
}

func readEntry(row *sql.Row) (Entry, error) {
	var e Entry
	var deadline int64
	err := row.Scan(&e.Execution, &e.Target, &e.Digest, &e.Document, &deadline, &e.State, &e.Sequence)
	if err == nil {
		e.Expires = time.UnixMilli(deadline).UTC()
	}
	return e, err
}

// Accept grants execution to exactly one caller. A replay returns false even
// if the previously accepted execution has already finished.
func (l *Ledger) Accept(ctx context.Context, r Request, now time.Time) (bool, error) {
	if r.Execution == "" || r.Target == "" || r.Digest == "" || r.Expires.IsZero() || now.IsZero() {
		return false, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	e, err := readEntry(tx.QueryRowContext(ctx, `SELECT id,target,digest,document,expires_ms,state,sequence FROM executions WHERE id=?`, r.Execution))
	switch {
	case err == nil:
		if e.Target != r.Target || e.Digest != r.Digest || e.Document != r.Document || e.Expires.UnixMilli() != r.Expires.UnixMilli() {
			return false, ErrConflict
		}
		return false, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	var cutoff int64
	if err := tx.QueryRowContext(ctx, "SELECT cutoff_ms FROM retention WHERE singleton=1").Scan(&cutoff); err != nil {
		return false, err
	}
	if r.Expires.UnixMilli() <= max(now.UnixMilli(), cutoff) {
		return false, ErrExpired
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO executions(id,target,digest,document,expires_ms,state) VALUES (?,?,?,?,?,?)
 ON CONFLICT(target) WHERE state NOT IN ('succeeded','failed','canceled','abandoned') DO NOTHING`,
		r.Execution, r.Target, r.Digest, r.Document, r.Expires.UnixMilli(), deployment.Dispatching)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, ErrBusy
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// NextAccepted only returns work that never passed the durable execution gate.
func (l *Ledger) NextAccepted(ctx context.Context) (Entry, error) {
	return readEntry(l.db.QueryRowContext(ctx, `SELECT id,target,digest,document,expires_ms,state,sequence
 FROM executions WHERE state=? AND document<>'' ORDER BY rowid LIMIT 1`, deployment.Dispatching))
}
