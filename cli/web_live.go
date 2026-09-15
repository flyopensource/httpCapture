package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type liveRequestSummary struct {
	SessionID       string `json:"sessionId"`
	RequestID       int64  `json:"requestId"`
	TimestampMillis int64  `json:"timestampMillis"`
	Method          string `json:"method"`
	URL             string `json:"url"`
	Status          int    `json:"status"`
	DurationMillis  int64  `json:"durationMillis"`
}

func (index *webIndex) liveRevision(ctx context.Context) (string, error) {
	rows, err := index.db.QueryContext(ctx, `SELECT s.id, s.status, s.traffic_mtime_ns, s.traffic_size,
		s.index_error, COALESCE(c.device, 0), COALESCE(c.inode, 0),
		COALESCE(c.committed_offset, 0), COALESCE(z.epoch, 0),
		(SELECT COUNT(*) FROM requests r WHERE r.session_id = s.id)
		FROM sessions s LEFT JOIN traffic_cursors c ON c.session_id = s.id
		LEFT JOIN traffic_resets z ON z.session_id = s.id ORDER BY s.id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var id, status, indexError string
		var mtime, size, device, inode, offset, epoch, count int64
		if err := rows.Scan(&id, &status, &mtime, &size, &indexError, &device, &inode, &offset, &epoch, &count); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%q:%q:%d:%d:%q:%d:%d:%d:%d:%d\n", id, status, mtime, size,
			indexError, device, inode, offset, epoch, count)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (index *webIndex) liveResetEpochs(ctx context.Context) (map[string]int64, error) {
	rows, err := index.db.QueryContext(ctx, `SELECT session_id, epoch FROM traffic_resets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int64)
	for rows.Next() {
		var id string
		var epoch int64
		if err := rows.Scan(&id, &epoch); err != nil {
			return nil, err
		}
		result[id] = epoch
	}
	return result, rows.Err()
}

func (index *webIndex) latestLiveRequestID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := index.db.QueryRowContext(ctx, `SELECT MAX(id) FROM requests`).Scan(&id)
	return id.Int64, err
}

func (index *webIndex) liveRequestsAfter(ctx context.Context, after int64, limit int) ([]liveRequestSummary, error) {
	rows, err := index.db.QueryContext(ctx, `SELECT id, session_id, timestamp_ms, method, url, status, duration_ms
		FROM requests WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]liveRequestSummary, 0, limit)
	for rows.Next() {
		var item liveRequestSummary
		if err := rows.Scan(&item.RequestID, &item.SessionID, &item.TimestampMillis,
			&item.Method, &item.URL, &item.Status, &item.DurationMillis); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (app *webApplication) handleLiveEvents(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeAPIError(response, http.StatusInternalServerError, "服务端不支持实时流")
		return
	}
	ctx := request.Context()
	if err := app.index.rescan(ctx); err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	revision, err := app.index.liveRevision(ctx)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	lastID, err := app.index.latestLiveRequestID(ctx)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	resetEpochs, err := app.index.liveResetEpochs(ctx)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	if !writeLiveEvent(response, flusher, "refresh", map[string]string{"reason": "connected"}) {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := app.index.rescan(ctx); err != nil {
				writeLiveEvent(response, flusher, "interrupted", map[string]string{"error": "实时索引中断，请手动重新扫描"})
				return
			}
			current, err := app.index.liveRevision(ctx)
			if err != nil {
				writeLiveEvent(response, flusher, "interrupted", map[string]string{"error": "实时索引状态读取失败"})
				return
			}
			if current == revision {
				_, _ = response.Write([]byte(": heartbeat\n\n"))
				flusher.Flush()
				continue
			}
			revision = current
			newEpochs, err := app.index.liveResetEpochs(ctx)
			if err != nil {
				return
			}
			var resetSessions []string
			for id, epoch := range newEpochs {
				if epoch != resetEpochs[id] {
					resetSessions = append(resetSessions, id)
				}
			}
			resetEpochs = newEpochs
			if len(resetSessions) > 0 && !writeLiveEvent(response, flusher, "reset", resetSessions) {
				return
			}
			latest, err := app.index.latestLiveRequestID(ctx)
			if err != nil {
				return
			}
			if latest < lastID {
				lastID = latest
				if !writeLiveEvent(response, flusher, "refresh", map[string]string{"reason": "index-reset"}) {
					return
				}
				continue
			}
			for lastID < latest {
				items, err := app.index.liveRequestsAfter(ctx, lastID, 250)
				if err != nil {
					return
				}
				if len(items) == 0 {
					break
				}
				lastID = items[len(items)-1].RequestID
				if !writeLiveEvent(response, flusher, "requests", items) {
					return
				}
			}
			if !writeLiveEvent(response, flusher, "refresh", map[string]string{"reason": "index-changed"}) {
				return
			}
		}
	}
}

func writeLiveEvent(response http.ResponseWriter, flusher http.Flusher, name string, value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(response, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
