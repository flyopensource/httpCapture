package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type trafficCursor struct {
	device, inode uint64
	offset        int64
	lineNumber    int
	skipped       int
	headHash      string
	tailHash      string
}

func (index *webIndex) updateTraffic(ctx context.Context, id, path string, info os.FileInfo, previous sourceFingerprint, exists bool) error {
	if info == nil || !exists || info.Size() <= previous.TrafficSize {
		return index.rebuildTraffic(ctx, id, path, info)
	}
	cursor, err := index.loadTrafficCursor(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return index.rebuildTraffic(ctx, id, path, info)
	}
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	device, inode, known := sourceDeviceInode(opened)
	if !known || !os.SameFile(info, opened) || cursor.device != device || cursor.inode != inode || opened.Size() < cursor.offset {
		return index.rebuildTraffic(ctx, id, path, info)
	}
	head, tail, err := trafficWindowHashes(file, cursor.offset)
	if err != nil {
		return err
	}
	if head != cursor.headHash || tail != cursor.tailHash {
		return index.rebuildTraffic(ctx, id, path, info)
	}
	return index.scanTraffic(ctx, id, file, trafficCursor{
		device: device, inode: inode, offset: cursor.offset,
		lineNumber: cursor.lineNumber, skipped: cursor.skipped,
	}, false)
}

func (index *webIndex) rebuildTraffic(ctx context.Context, id, path string, info os.FileInfo) error {
	if info == nil {
		return index.scanTraffic(ctx, id, nil, trafficCursor{}, true)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errors.New("抓包文件扫描时已替换，请重试")
	}
	device, inode, _ := sourceDeviceInode(opened)
	return index.scanTraffic(ctx, id, file, trafficCursor{device: device, inode: inode}, true)
}

func (index *webIndex) loadTrafficCursor(ctx context.Context, id string) (trafficCursor, error) {
	var cursor trafficCursor
	err := index.db.QueryRowContext(ctx, `SELECT device, inode, committed_offset, line_number, skipped_count, head_hash, tail_hash
		FROM traffic_cursors WHERE session_id = ?`, id).Scan(&cursor.device, &cursor.inode,
		&cursor.offset, &cursor.lineNumber, &cursor.skipped, &cursor.headHash, &cursor.tailHash)
	return cursor, err
}

func (index *webIndex) scanTraffic(ctx context.Context, id string, file *os.File, cursor trafficCursor, reset bool) error {
	transaction, err := index.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if reset {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM request_fts WHERE rowid IN (SELECT id FROM requests WHERE session_id = ?)`, id); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `DELETE FROM requests WHERE session_id = ?`, id); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO traffic_resets(session_id, epoch) VALUES (?, 1)
			ON CONFLICT(session_id) DO UPDATE SET epoch = epoch + 1`, id); err != nil {
			return err
		}
	}
	if file != nil {
		if _, err := file.Seek(cursor.offset, io.SeekStart); err != nil {
			return err
		}
		reader := bufio.NewReaderSize(file, 64*1024)
		for {
			line, consumed, oversized, readErr := readJSONLine(reader, maxIndexableJSONLine)
			// An unfinished final line remains invisible and will be retried from
			// the committed offset when its newline arrives.
			if errors.Is(readErr, io.EOF) && consumed > 0 && !bytes.HasSuffix(line, []byte{'\n'}) {
				break
			}
			if consumed > 0 {
				cursor.lineNumber++
				if oversized {
					cursor.skipped++
				} else if len(bytes.TrimSpace(line)) > 0 {
					var captured capturedTransaction
					if err := json.Unmarshal(line, &captured); err != nil || captured.SchemaVersion != captureSchemaVersion {
						cursor.skipped++
					} else if err := insertIndexedRequest(ctx, transaction, id, cursor.lineNumber, cursor.offset, consumed, captured); err != nil {
						return err
					}
				}
				cursor.offset += consumed
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		head, tail, err := trafficWindowHashes(file, cursor.offset)
		if err != nil {
			return err
		}
		cursor.headHash, cursor.tailHash = head, tail
	} else {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM traffic_cursors WHERE session_id = ?`, id); err != nil {
			return err
		}
	}
	indexError := ""
	if cursor.skipped > 0 {
		indexError = fmt.Sprintf("跳过 %d 条损坏或过大的 JSONL 记录", cursor.skipped)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE sessions SET index_error = ?, indexed_at_ms = ? WHERE id = ?`, indexError, time.Now().UnixMilli(), id); err != nil {
		return err
	}
	if file != nil {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO traffic_cursors
			(session_id, device, inode, committed_offset, line_number, skipped_count, head_hash, tail_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET device=excluded.device, inode=excluded.inode,
			committed_offset=excluded.committed_offset, line_number=excluded.line_number,
			skipped_count=excluded.skipped_count, head_hash=excluded.head_hash, tail_hash=excluded.tail_hash`,
			id, cursor.device, cursor.inode, cursor.offset, cursor.lineNumber, cursor.skipped,
			cursor.headHash, cursor.tailHash); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func trafficWindowHashes(file *os.File, offset int64) (string, string, error) {
	if offset == 0 {
		return "", "", nil
	}
	const window int64 = 4096
	headSize := min(offset, window)
	tailStart := max(int64(0), offset-window)
	head := make([]byte, headSize)
	tail := make([]byte, offset-tailStart)
	if _, err := file.ReadAt(head, 0); err != nil {
		return "", "", err
	}
	if _, err := file.ReadAt(tail, tailStart); err != nil {
		return "", "", err
	}
	headHash := sha256.Sum256(head)
	tailHash := sha256.Sum256(tail)
	return hex.EncodeToString(headHash[:]), hex.EncodeToString(tailHash[:]), nil
}
