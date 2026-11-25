package storage

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type NodeOperationHistoryEntry struct {
	ID        string         `json:"id"`
	Action    nodeops.Action `json:"action"`
	State     nodeops.State  `json:"state"`
	Target    string         `json:"target,omitempty"`
	Error     string         `json:"error,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func nodeOperationHistoryTarget(req nodeops.Request) string {
	switch req.Action {
	case nodeops.ContainerCreate, nodeops.ContainerRelease, nodeops.VolumeCreate, nodeops.NetworkCreate:
		return req.Name
	case nodeops.RegistryPull, nodeops.ImagePush, nodeops.ImageTag:
		return req.Image
	case nodeops.ImageDelete:
		return req.ImageID
	default:
		return req.Target
	}
}

type NodeOperationHistoryPage struct {
	Items []NodeOperationHistoryEntry `json:"items"`
	Next  string                      `json:"next,omitempty"`
}

func (s *Store) ListNodeOperationHistory(ctx context.Context, token, agent, id string, now time.Time) ([]NodeOperationHistoryEntry, error) {
	page, err := s.listNodeOperationHistory(ctx, token, agent, id, "", 50, now)
	return page.Items, err
}

func (s *Store) PageNodeOperationHistory(ctx context.Context, token, agent, after string, limit int, now time.Time) (NodeOperationHistoryPage, error) {
	return s.listNodeOperationHistory(ctx, token, agent, "", after, limit, now)
}

func (s *Store) listNodeOperationHistory(ctx context.Context, token, agent, id, after string, limit int, now time.Time) (NodeOperationHistoryPage, error) {
	if agent == "" || len(agent) > 128 || limit < 1 || limit > 50 {
		return NodeOperationHistoryPage{}, ErrInvalid
	}
	if id != "" {
		if _, err := hex.DecodeString(id); len(id) != 32 || err != nil {
			return NodeOperationHistoryPage{}, ErrInvalid
		}
	}
	var cursorTime, cursorRow int64
	if after != "" {
		timestamp, row, ok := strings.Cut(after, ".")
		var timestampErr, rowErr error
		cursorTime, timestampErr = strconv.ParseInt(timestamp, 10, 64)
		cursorRow, rowErr = strconv.ParseInt(row, 10, 64)
		if !ok || timestampErr != nil || rowErr != nil || cursorTime <= 0 || cursorRow <= 0 {
			return NodeOperationHistoryPage{}, ErrInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeOperationHistoryPage{}, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, token)
	if err != nil {
		return NodeOperationHistoryPage{}, err
	}
	if err = retireExpiredNodeOperations(ctx, tx, agent, now); err != nil {
		return NodeOperationHistoryPage{}, err
	}
	query := `SELECT id,action,state,history_target,error,created_ms,rowid FROM node_operations WHERE agent=? AND actor=? AND mutation=1 AND created_ms>=?`
	args := []any{agent, actor, now.Add(-30 * 24 * time.Hour).UnixMilli()}
	if id != "" {
		query += ` AND id=?`
		args = append(args, id)
	}
	if after != "" {
		query += ` AND (created_ms,rowid) < (?,?)`
		args = append(args, cursorTime, cursorRow)
	}
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query+` ORDER BY created_ms DESC,rowid DESC LIMIT ?`, args...)
	if err != nil {
		return NodeOperationHistoryPage{}, err
	}
	defer rows.Close()
	page := NodeOperationHistoryPage{Items: make([]NodeOperationHistoryEntry, 0, limit)}
	var lastCreated, lastRow int64
	for rows.Next() {
		var entry NodeOperationHistoryEntry
		var created, row int64
		if err = rows.Scan(&entry.ID, &entry.Action, &entry.State, &entry.Target, &entry.Error, &created, &row); err != nil {
			return NodeOperationHistoryPage{}, err
		}
		if len(page.Items) == limit {
			page.Next = strconv.FormatInt(lastCreated, 10) + "." + strconv.FormatInt(lastRow, 10)
			break
		}
		entry.CreatedAt = time.UnixMilli(created).UTC()
		page.Items = append(page.Items, entry)
		lastCreated, lastRow = created, row
	}
	if err = rows.Err(); err != nil {
		return NodeOperationHistoryPage{}, err
	}
	if err := rows.Close(); err != nil {
		return NodeOperationHistoryPage{}, err
	}
	return page, tx.Commit()
}
