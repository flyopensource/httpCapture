package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	webIndexFileName       = ".httpcapture-index.sqlite"
	maxSessionMetadataSize = 1 << 20
	maxIndexableJSONLine   = 32 << 20
	maxFTSBodyBytes        = 256 << 10
	maxDisplayBodyBytes    = 512 << 10
)

type webIndex struct {
	root              string
	db                *sql.DB
	mu                sync.RWMutex
	liveSourceAllowed func(sessionState) bool
}

type trafficScanOptions struct {
	startOffset int64
	startMS     int64
	clientIP    string
}

type indexedSession struct {
	ID                  string   `json:"id"`
	CaptureID           string   `json:"captureId"`
	Engine              string   `json:"engine"`
	EngineVersion       string   `json:"engineVersion,omitempty"`
	EngineRevision      string   `json:"engineRevision,omitempty"`
	Packages            []string `json:"packages"`
	DeviceName          string   `json:"deviceName,omitempty"`
	ClientIP            string   `json:"clientIp,omitempty"`
	ProxyHost           string   `json:"proxyHost,omitempty"`
	ProxyPort           int      `json:"proxyPort,omitempty"`
	StartTimeMillis     int64    `json:"startTimeMillis"`
	EndTimeMillis       int64    `json:"endTimeMillis,omitempty"`
	Status              string   `json:"status"`
	RequestCount        int      `json:"requestCount"`
	SkippedCount        int      `json:"skippedCount,omitempty"`
	IndexedRequestCount int      `json:"indexedRequestCount"`
	TrafficSize         int64    `json:"trafficSize"`
	HasHAR              bool     `json:"hasHar"`
	IndexError          string   `json:"indexError,omitempty"`
}

type requestQuery struct {
	SessionID     string
	Search        string
	Method        string
	Status        string
	ContentType   string
	FocusMode     string
	FocusRules    []focusRule
	FromMS        int64
	ToMS          int64
	MinDurationMS int64
	MaxDurationMS int64
	MinSize       int64
	MaxSize       int64
	SortAscending bool
	Page          int
	PageSize      int
}

type requestSummary struct {
	ID           int64  `json:"id"`
	TimestampMS  int64  `json:"timestampMillis"`
	Method       string `json:"method"`
	URL          string `json:"url"`
	Host         string `json:"host"`
	Path         string `json:"path"`
	Status       int    `json:"status"`
	ContentType  string `json:"contentType,omitempty"`
	DurationMS   int64  `json:"durationMillis"`
	RequestSize  int64  `json:"requestSize"`
	ResponseSize int64  `json:"responseSize"`
	TotalSize    int64  `json:"totalSize"`
	Focused      bool   `json:"focused,omitempty"`
}

type requestPage struct {
	Items    []requestSummary `json:"items"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

type payloadView struct {
	DeclaredSize     int64  `json:"declaredSize"`
	CapturedSize     int    `json:"capturedSize"`
	Encoding         string `json:"encoding,omitempty"`
	Data             string `json:"data,omitempty"`
	Truncated        bool   `json:"truncated,omitempty"`
	DisplayTruncated bool   `json:"displayTruncated,omitempty"`
	DecodedEncoding  string `json:"decodedEncoding,omitempty"`
	DisplayError     string `json:"displayError,omitempty"`
	ReadError        string `json:"readError,omitempty"`
	DownloadURL      string `json:"downloadUrl,omitempty"`
}

type requestDetail struct {
	ID              int64               `json:"id"`
	SessionID       string              `json:"sessionId"`
	Timestamp       string              `json:"timestamp"`
	TimestampMillis int64               `json:"timestampMillis"`
	DurationMillis  int64               `json:"durationMillis"`
	ClientAddress   string              `json:"clientAddress,omitempty"`
	Method          string              `json:"method"`
	URL             string              `json:"url"`
	HTTPVersion     string              `json:"httpVersion"`
	RequestHeaders  map[string][]string `json:"requestHeaders"`
	Query           map[string][]string `json:"query"`
	RequestBody     payloadView         `json:"requestBody"`
	StatusCode      int                 `json:"statusCode"`
	Status          string              `json:"status"`
	ResponseVersion string              `json:"responseHttpVersion"`
	ResponseHeaders map[string][]string `json:"responseHeaders"`
	ResponseBody    payloadView         `json:"responseBody"`
}

type sourceFingerprint struct {
	MetaMTimeNS    int64
	MetaSize       int64
	TrafficMTimeNS int64
	TrafficSize    int64
}

func defaultSessionsRoot() (string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(workingDirectory, "httpcapture-sessions"), nil
}

func openWebIndex(root string) (*webIndex, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("创建会话目录: %w", err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, err
	}
	databasePath := filepath.Join(absolute, webIndexFileName)
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	index := &webIndex{root: absolute, db: database, liveSourceAllowed: isCurrentActiveSession}
	if err := index.initialize(); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		_ = database.Close()
		return nil, err
	}
	return index, nil
}

func (index *webIndex) close() error { return index.db.Close() }

func (index *webIndex) initialize() error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = DELETE`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA temp_store = MEMORY`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			capture_id TEXT NOT NULL,
			directory TEXT NOT NULL,
			engine TEXT NOT NULL,
			engine_version TEXT NOT NULL DEFAULT '',
			engine_revision TEXT NOT NULL DEFAULT '',
			packages_json TEXT NOT NULL,
			device_name TEXT NOT NULL DEFAULT '',
			client_ip TEXT NOT NULL DEFAULT '',
			proxy_host TEXT NOT NULL DEFAULT '',
			proxy_port INTEGER NOT NULL DEFAULT 0,
			start_ms INTEGER NOT NULL DEFAULT 0,
			end_ms INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			request_count INTEGER NOT NULL DEFAULT 0,
			skipped_count INTEGER NOT NULL DEFAULT 0,
			meta_mtime_ns INTEGER NOT NULL DEFAULT 0,
			meta_size INTEGER NOT NULL DEFAULT 0,
			traffic_mtime_ns INTEGER NOT NULL DEFAULT 0,
			traffic_size INTEGER NOT NULL DEFAULT 0,
			has_har INTEGER NOT NULL DEFAULT 0,
			index_error TEXT NOT NULL DEFAULT '',
			indexed_at_ms INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			line_number INTEGER NOT NULL,
			byte_offset INTEGER NOT NULL,
			byte_length INTEGER NOT NULL,
			timestamp_ms INTEGER NOT NULL,
			method TEXT NOT NULL,
			url TEXT NOT NULL,
			host TEXT NOT NULL,
			path TEXT NOT NULL,
			status INTEGER NOT NULL,
			content_type TEXT NOT NULL,
			duration_ms INTEGER NOT NULL,
			request_size INTEGER NOT NULL,
			response_size INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS requests_session_time ON requests(session_id, timestamp_ms)`,
		`CREATE INDEX IF NOT EXISTS requests_filters ON requests(session_id, method, status, content_type)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS request_fts USING fts5(content, tokenize = 'unicode61 remove_diacritics 2')`,
		`CREATE TABLE IF NOT EXISTS traffic_cursors (
			session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
			device INTEGER NOT NULL,
			inode INTEGER NOT NULL,
			committed_offset INTEGER NOT NULL,
			line_number INTEGER NOT NULL,
			skipped_count INTEGER NOT NULL,
			head_hash TEXT NOT NULL,
			tail_hash TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS traffic_resets (
			session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
			epoch INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := index.db.Exec(statement); err != nil {
			return fmt.Errorf("初始化 SQLite 索引: %w", err)
		}
	}
	return nil
}

func (index *webIndex) rescan(ctx context.Context) error {
	index.mu.Lock()
	defer index.mu.Unlock()
	entries, err := os.ReadDir(index.root)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if err := validateSessionID(entry.Name()); err != nil {
			continue
		}
		directory := filepath.Join(index.root, entry.Name())
		metadataPath := filepath.Join(directory, "meta.json")
		metadataInfo, err := regularFileInfo(metadataPath)
		if err != nil {
			continue
		}
		metadataBytes, err := readLimitedFile(metadataPath, maxSessionMetadataSize)
		if err != nil {
			continue
		}
		var metadata sessionState
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue
		}
		seen[entry.Name()] = true
		trafficPath, scanOptions := index.sessionTrafficSource(directory, metadata)
		trafficInfo, trafficErr := regularFileInfo(trafficPath)
		if trafficErr != nil && !errors.Is(trafficErr, os.ErrNotExist) {
			trafficInfo = nil
		}
		fingerprint := sourceFingerprint{MetaMTimeNS: metadataInfo.ModTime().UnixNano(), MetaSize: metadataInfo.Size()}
		if trafficInfo != nil {
			fingerprint.TrafficMTimeNS = trafficInfo.ModTime().UnixNano()
			fingerprint.TrafficSize = trafficInfo.Size()
		}
		harInfo, harErr := regularFileInfo(filepath.Join(directory, "session.har"))
		hasHAR := harErr == nil && harInfo.Size() > 0
		previous, exists, err := index.fingerprint(ctx, entry.Name())
		if err != nil {
			return err
		}
		trafficChanged := !exists || previous.TrafficMTimeNS != fingerprint.TrafficMTimeNS || previous.TrafficSize != fingerprint.TrafficSize
		if !trafficChanged && trafficInfo != nil {
			cursor, cursorErr := index.loadTrafficCursor(ctx, entry.Name())
			device, inode, known := sourceDeviceInode(trafficInfo)
			if cursorErr != nil && !errors.Is(cursorErr, sql.ErrNoRows) {
				return cursorErr
			}
			trafficChanged = errors.Is(cursorErr, sql.ErrNoRows) || !known || cursor.device != device || cursor.inode != inode
		}
		if err := index.upsertSession(ctx, entry.Name(), directory, metadata, fingerprint, hasHAR); err != nil {
			return err
		}
		if trafficChanged {
			if err := index.updateTraffic(ctx, entry.Name(), trafficPath, trafficInfo, previous, exists, scanOptions); err != nil {
				if updateErr := index.setIndexError(ctx, entry.Name(), err.Error()); updateErr != nil {
					return updateErr
				}
			}
		}
	}
	return index.removeMissingSessions(ctx, seen)
}

func (index *webIndex) fingerprint(ctx context.Context, id string) (sourceFingerprint, bool, error) {
	var result sourceFingerprint
	err := index.db.QueryRowContext(ctx, "SELECT meta_mtime_ns, meta_size, traffic_mtime_ns, traffic_size FROM sessions WHERE id = ?", id).
		Scan(&result.MetaMTimeNS, &result.MetaSize, &result.TrafficMTimeNS, &result.TrafficSize)
	if errors.Is(err, sql.ErrNoRows) {
		return sourceFingerprint{}, false, nil
	}
	return result, err == nil, err
}

func (index *webIndex) upsertSession(ctx context.Context, id, directory string, metadata sessionState, fingerprint sourceFingerprint, hasHAR bool) error {
	packages, err := json.Marshal(metadata.Packages)
	if err != nil {
		return err
	}
	_, err = index.db.ExecContext(ctx, `INSERT INTO sessions (
		id, capture_id, directory, engine, engine_version, engine_revision, packages_json,
		device_name, client_ip, proxy_host, proxy_port, start_ms, end_ms, status,
		request_count, skipped_count, meta_mtime_ns, meta_size, traffic_mtime_ns,
		traffic_size, has_har, indexed_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		capture_id=excluded.capture_id, directory=excluded.directory, engine=excluded.engine,
		engine_version=excluded.engine_version, engine_revision=excluded.engine_revision,
		packages_json=excluded.packages_json, device_name=excluded.device_name,
		client_ip=excluded.client_ip, proxy_host=excluded.proxy_host, proxy_port=excluded.proxy_port,
		start_ms=excluded.start_ms, end_ms=excluded.end_ms, status=excluded.status,
		request_count=excluded.request_count, skipped_count=excluded.skipped_count,
		meta_mtime_ns=excluded.meta_mtime_ns, meta_size=excluded.meta_size,
		traffic_mtime_ns=excluded.traffic_mtime_ns, traffic_size=excluded.traffic_size,
		has_har=excluded.has_har, indexed_at_ms=excluded.indexed_at_ms`,
		id, valueOrDefault(metadata.CaptureID, id), directory, metadata.Engine, metadata.EngineVersion,
		metadata.EngineRevision, string(packages), metadata.DeviceName, metadata.ClientIP,
		metadata.ProxyHost, metadata.ProxyPort, metadata.StartedMS, metadata.StoppedMS,
		metadata.Status, metadata.RequestCount, metadata.SkippedCount,
		fingerprint.MetaMTimeNS, fingerprint.MetaSize, fingerprint.TrafficMTimeNS,
		fingerprint.TrafficSize, boolInt(hasHAR), time.Now().UnixMilli())
	return err
}

func (index *webIndex) reindexTraffic(ctx context.Context, sessionID, trafficPath string, trafficInfo os.FileInfo) error {
	return index.rebuildTraffic(ctx, sessionID, trafficPath, trafficInfo, trafficScanOptions{})
}

func (index *webIndex) sessionTrafficSource(directory string, metadata sessionState) (string, trafficScanOptions) {
	if metadata.Engine == engineProxify && metadata.Status == "recording" &&
		metadata.TrafficSource != "" && index.liveSourceAllowed != nil && index.liveSourceAllowed(metadata) {
		return metadata.TrafficSource, trafficScanOptions{
			startOffset: metadata.StartOffset,
			startMS:     metadata.StartedMS,
			clientIP:    metadata.ClientIP,
		}
	}
	return filepath.Join(directory, "traffic.jsonl"), trafficScanOptions{}
}

func isCurrentActiveSession(metadata sessionState) bool {
	active, err := readState()
	if err != nil {
		return false
	}
	return active.Status == "recording" && active.CaptureID == metadata.CaptureID &&
		filepath.Clean(active.SessionDir) == filepath.Clean(metadata.SessionDir) &&
		active.TrafficSource == metadata.TrafficSource && active.StartOffset == metadata.StartOffset &&
		active.StartedMS == metadata.StartedMS && active.ClientIP == metadata.ClientIP
}

func insertIndexedRequest(ctx context.Context, transaction *sql.Tx, sessionID string, lineNumber int, offset, length int64, captured capturedTransaction) error {
	parsedURL, _ := url.Parse(captured.Request.URL)
	host := ""
	path := ""
	if parsedURL != nil {
		host = parsedURL.Hostname()
		path = parsedURL.EscapedPath()
		if path == "" {
			path = "/"
		}
	}
	contentType := firstHeader(captured.Response.Headers, "Content-Type")
	if contentType == "" {
		contentType = firstHeader(captured.Request.Headers, "Content-Type")
	}
	requestSize := payloadSize(captured.Request.Body)
	responseSize := payloadSize(captured.Response.Body)
	result, err := transaction.ExecContext(ctx, `INSERT INTO requests (
		session_id, line_number, byte_offset, byte_length, timestamp_ms, method, url, host,
		path, status, content_type, duration_ms, request_size, response_size
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, lineNumber, offset, length, captured.TimestampMillis, captured.Request.Method,
		captured.Request.URL, host, path, captured.Response.StatusCode, contentType,
		captured.DurationMillis, requestSize, responseSize)
	if err != nil {
		return err
	}
	requestID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = transaction.ExecContext(ctx, `INSERT INTO request_fts(rowid, content) VALUES (?, ?)`, requestID, searchableTransaction(captured, host, path))
	return err
}

func searchableTransaction(captured capturedTransaction, host, path string) string {
	parts := []string{
		captured.Request.Method, captured.Request.URL, host, path,
		strconv.Itoa(captured.Response.StatusCode), captured.Response.Status,
		headersForSearch(captured.Request.Headers), headersForSearch(captured.Response.Headers),
		payloadForSearch(captured.Request.Body), payloadForSearch(captured.Response.Body),
	}
	return strings.Join(parts, "\n")
}

func headersForSearch(headers map[string][]string) string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		for _, value := range headers[key] {
			builder.WriteString(key)
			builder.WriteString(": ")
			builder.WriteString(value)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func payloadForSearch(payload capturedPayload) string {
	if payload.Encoding != "utf8" || payload.Data == "" {
		return ""
	}
	return truncateUTF8(payload.Data, maxFTSBodyBytes)
}

func (index *webIndex) setIndexError(ctx context.Context, sessionID, message string) error {
	_, err := index.db.ExecContext(ctx, "UPDATE sessions SET index_error = ?, traffic_mtime_ns = -1, traffic_size = -1, indexed_at_ms = ? WHERE id = ?", message, time.Now().UnixMilli(), sessionID)
	return err
}

func (index *webIndex) removeMissingSessions(ctx context.Context, seen map[string]bool) error {
	rows, err := index.db.QueryContext(ctx, `SELECT id FROM sessions`)
	if err != nil {
		return err
	}
	var missing []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range missing {
		if err := index.deleteIndexedSession(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (index *webIndex) deleteIndexedSession(ctx context.Context, id string) error {
	transaction, err := index.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM request_fts WHERE rowid IN (SELECT id FROM requests WHERE session_id = ?)`, id); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return err
	}
	return transaction.Commit()
}

func (index *webIndex) listSessions(ctx context.Context, engine, packageName, device string, fromMS, toMS int64) ([]indexedSession, error) {
	query := `SELECT s.id, s.capture_id, s.engine, s.engine_version, s.engine_revision,
		s.packages_json, s.device_name, s.client_ip, s.proxy_host, s.proxy_port,
		s.start_ms, s.end_ms, s.status, s.request_count, s.skipped_count,
		(SELECT COUNT(*) FROM requests r WHERE r.session_id = s.id), s.traffic_size,
		s.has_har, s.index_error FROM sessions s WHERE 1=1`
	arguments := make([]any, 0)
	if engine != "" {
		query += ` AND s.engine = ?`
		arguments = append(arguments, engine)
	}
	if packageName != "" {
		query += ` AND s.packages_json LIKE ? ESCAPE '\'`
		arguments = append(arguments, "%"+escapeLike(packageName)+"%")
	}
	if device != "" {
		query += ` AND s.device_name LIKE ? ESCAPE '\'`
		arguments = append(arguments, "%"+escapeLike(device)+"%")
	}
	if fromMS > 0 {
		query += ` AND s.start_ms >= ?`
		arguments = append(arguments, fromMS)
	}
	if toMS > 0 {
		query += ` AND s.start_ms <= ?`
		arguments = append(arguments, toMS)
	}
	query += ` ORDER BY s.start_ms DESC, s.id DESC`
	rows, err := index.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]indexedSession, 0)
	for rows.Next() {
		var session indexedSession
		var packagesJSON string
		var hasHAR int
		if err := rows.Scan(&session.ID, &session.CaptureID, &session.Engine, &session.EngineVersion,
			&session.EngineRevision, &packagesJSON, &session.DeviceName, &session.ClientIP,
			&session.ProxyHost, &session.ProxyPort, &session.StartTimeMillis, &session.EndTimeMillis,
			&session.Status, &session.RequestCount, &session.SkippedCount,
			&session.IndexedRequestCount, &session.TrafficSize, &hasHAR, &session.IndexError); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(packagesJSON), &session.Packages)
		if session.Packages == nil {
			session.Packages = []string{}
		}
		session.HasHAR = hasHAR != 0
		result = append(result, session)
	}
	return result, rows.Err()
}

func (index *webIndex) listRequests(ctx context.Context, filter requestQuery) (requestPage, error) {
	if err := validateSessionID(filter.SessionID); err != nil {
		return requestPage{}, err
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 100
	}
	if filter.PageSize > 500 {
		filter.PageSize = 500
	}
	focusMode := strings.ToLower(strings.TrimSpace(filter.FocusMode))
	if focusMode == "" {
		focusMode = "all"
	}
	if focusMode != "all" && focusMode != "only" && focusMode != "exclude" {
		return requestPage{}, errors.New("Focus 模式必须是 all、only 或 exclude")
	}
	where := []string{"r.session_id = ?"}
	arguments := []any{filter.SessionID}
	join := ""
	if strings.TrimSpace(filter.Search) != "" {
		ftsQuery := buildFTSQuery(filter.Search)
		if ftsQuery != "" {
			join = " JOIN request_fts f ON f.rowid = r.id"
			where = append(where, "f.content MATCH ?")
			arguments = append(arguments, ftsQuery)
		}
	}
	if filter.Method != "" {
		where = append(where, "r.method = ?")
		arguments = append(arguments, strings.ToUpper(filter.Method))
	}
	if filter.Status != "" {
		if strings.HasSuffix(strings.ToLower(filter.Status), "xx") && len(filter.Status) == 3 {
			class, err := strconv.Atoi(filter.Status[:1])
			if err != nil || class < 1 || class > 5 {
				return requestPage{}, errors.New("状态码筛选必须是整数或 2xx 形式")
			}
			where = append(where, "r.status >= ? AND r.status < ?")
			arguments = append(arguments, class*100, (class+1)*100)
		} else {
			statusCode, err := strconv.Atoi(filter.Status)
			if err != nil || statusCode < 100 || statusCode > 599 {
				return requestPage{}, errors.New("状态码筛选必须是 100..599 或 2xx 形式")
			}
			where = append(where, "r.status = ?")
			arguments = append(arguments, statusCode)
		}
	}
	if filter.ContentType != "" {
		where = append(where, "r.content_type LIKE ? ESCAPE '\\'")
		arguments = append(arguments, "%"+escapeLike(filter.ContentType)+"%")
	}
	addIntRange := func(column string, minimum, maximum int64) {
		if minimum > 0 {
			where = append(where, column+" >= ?")
			arguments = append(arguments, minimum)
		}
		if maximum > 0 {
			where = append(where, column+" <= ?")
			arguments = append(arguments, maximum)
		}
	}
	addIntRange("r.timestamp_ms", filter.FromMS, filter.ToMS)
	addIntRange("r.duration_ms", filter.MinDurationMS, filter.MaxDurationMS)
	addIntRange("(r.request_size + r.response_size)", filter.MinSize, filter.MaxSize)
	focusWhere, focusArguments := focusRulesSQL(filter.FocusRules)
	if focusMode == "only" {
		if focusWhere == "" {
			where = append(where, "0")
		} else {
			where = append(where, "("+focusWhere+")")
			arguments = append(arguments, focusArguments...)
		}
	} else if focusMode == "exclude" && focusWhere != "" {
		where = append(where, "NOT ("+focusWhere+")")
		arguments = append(arguments, focusArguments...)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := index.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests r"+join+" WHERE "+whereSQL, arguments...).Scan(&total); err != nil {
		return requestPage{}, err
	}
	sortOrder := "DESC"
	if filter.SortAscending {
		sortOrder = "ASC"
	}
	focusSelect := "0"
	focusSelectArguments := []any{}
	if focusWhere != "" {
		focusSelect = "CASE WHEN " + focusWhere + " THEN 1 ELSE 0 END"
		focusSelectArguments = append(focusSelectArguments, focusArguments...)
	}
	query := `SELECT r.id, r.timestamp_ms, r.method, r.url, r.host, r.path, r.status,
		r.content_type, r.duration_ms, r.request_size, r.response_size, ` + focusSelect + `
		FROM requests r` + join + ` WHERE ` + whereSQL + ` ORDER BY r.timestamp_ms ` + sortOrder + `, r.id ` + sortOrder + ` LIMIT ? OFFSET ?`
	queryArguments := append(append(append([]any{}, focusSelectArguments...), arguments...), filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := index.db.QueryContext(ctx, query, queryArguments...)
	if err != nil {
		return requestPage{}, err
	}
	defer rows.Close()
	items := make([]requestSummary, 0)
	for rows.Next() {
		var item requestSummary
		var focused int
		if err := rows.Scan(&item.ID, &item.TimestampMS, &item.Method, &item.URL, &item.Host,
			&item.Path, &item.Status, &item.ContentType, &item.DurationMS,
			&item.RequestSize, &item.ResponseSize, &focused); err != nil {
			return requestPage{}, err
		}
		item.TotalSize = item.RequestSize + item.ResponseSize
		item.Focused = focused != 0
		items = append(items, item)
	}
	return requestPage{Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize}, rows.Err()
}

func focusRulesSQL(rules []focusRule) (string, []any) {
	clauses := []string{}
	arguments := []any{}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		pattern := strings.TrimSpace(rule.Pattern)
		if pattern == "" {
			continue
		}
		like := "%" + escapeLike(pattern) + "%"
		switch rule.Type {
		case "host_contains":
			clauses = append(clauses, "r.host LIKE ? ESCAPE '\\'")
			arguments = append(arguments, like)
		case "path_contains":
			clauses = append(clauses, "r.path LIKE ? ESCAPE '\\'")
			arguments = append(arguments, like)
		case "method_url_contains":
			method := strings.ToUpper(strings.TrimSpace(rule.Method))
			if method == "" {
				clauses = append(clauses, "r.url LIKE ? ESCAPE '\\'")
				arguments = append(arguments, like)
			} else {
				clauses = append(clauses, "(r.method = ? AND r.url LIKE ? ESCAPE '\\')")
				arguments = append(arguments, method, like)
			}
		default:
			clauses = append(clauses, "r.url LIKE ? ESCAPE '\\'")
			arguments = append(arguments, like)
		}
	}
	return strings.Join(clauses, " OR "), arguments
}

func (index *webIndex) requestTransaction(ctx context.Context, sessionID string, requestID int64) (capturedTransaction, error) {
	if err := validateSessionID(sessionID); err != nil {
		return capturedTransaction{}, err
	}
	var directory string
	var offset, length int64
	var device, inode uint64
	err := index.db.QueryRowContext(ctx, `SELECT s.directory, r.byte_offset, r.byte_length, c.device, c.inode
		FROM requests r JOIN sessions s ON s.id = r.session_id
		JOIN traffic_cursors c ON c.session_id = r.session_id
		WHERE r.session_id = ? AND r.id = ?`, sessionID, requestID).Scan(&directory, &offset, &length, &device, &inode)
	if errors.Is(err, sql.ErrNoRows) {
		return capturedTransaction{}, os.ErrNotExist
	}
	if err != nil {
		return capturedTransaction{}, err
	}
	if length <= 0 || length > maxIndexableJSONLine {
		return capturedTransaction{}, errors.New("请求记录过大，无法在页面中打开")
	}
	trafficPath, err := index.indexedTrafficPath(directory, device, inode)
	if err != nil {
		return capturedTransaction{}, err
	}
	file, err := os.Open(trafficPath)
	if err != nil {
		return capturedTransaction{}, err
	}
	defer file.Close()
	content := make([]byte, length)
	if _, err := file.ReadAt(content, offset); err != nil && !errors.Is(err, io.EOF) {
		return capturedTransaction{}, err
	}
	var captured capturedTransaction
	if err := json.Unmarshal(bytes.TrimSpace(content), &captured); err != nil {
		return capturedTransaction{}, fmt.Errorf("请求原始记录已变化，请重新索引: %w", err)
	}
	return captured, nil
}

func (index *webIndex) indexedTrafficPath(directory string, expectedDevice, expectedInode uint64) (string, error) {
	archivePath := filepath.Join(directory, "traffic.jsonl")
	if err := ensurePathWithin(index.root, archivePath); err != nil {
		return "", err
	}
	candidates := []string{archivePath}
	metadataPath := filepath.Join(directory, "meta.json")
	if content, err := readLimitedFile(metadataPath, maxSessionMetadataSize); err == nil {
		var metadata sessionState
		if json.Unmarshal(content, &metadata) == nil && metadata.Engine == engineProxify && filepath.IsAbs(metadata.TrafficSource) {
			candidates = append(candidates, metadata.TrafficSource)
		}
	}
	for _, path := range candidates {
		info, err := regularFileInfo(path)
		if err != nil {
			continue
		}
		device, inode, known := sourceDeviceInode(info)
		if known && device == expectedDevice && inode == expectedInode {
			return path, nil
		}
	}
	return "", errors.New("请求原始记录已变化，请等待重新索引")
}

func (index *webIndex) requestDetail(ctx context.Context, sessionID string, requestID int64) (requestDetail, error) {
	captured, err := index.requestTransaction(ctx, sessionID, requestID)
	if err != nil {
		return requestDetail{}, err
	}
	parsedURL, _ := url.Parse(captured.Request.URL)
	query := map[string][]string{}
	if parsedURL != nil {
		query = parsedURL.Query()
	}
	return requestDetail{
		ID: requestID, SessionID: sessionID, Timestamp: captured.Timestamp,
		TimestampMillis: captured.TimestampMillis, DurationMillis: captured.DurationMillis,
		ClientAddress: captured.ClientAddress, Method: captured.Request.Method,
		URL: captured.Request.URL, HTTPVersion: captured.Request.HTTPVersion,
		RequestHeaders: captured.Request.Headers, Query: query,
		RequestBody: payloadForDisplay(captured.Request.Body, captured.Request.Headers, sessionID, requestID, "request"),
		StatusCode:  captured.Response.StatusCode, Status: captured.Response.Status,
		ResponseVersion: captured.Response.HTTPVersion, ResponseHeaders: captured.Response.Headers,
		ResponseBody: payloadForDisplay(captured.Response.Body, captured.Response.Headers, sessionID, requestID, "response"),
	}, nil
}

func payloadForDisplay(payload capturedPayload, headers map[string][]string, sessionID string, requestID int64, side string) payloadView {
	view := payloadView{
		DeclaredSize: payload.DeclaredSize, CapturedSize: payload.CapturedSize,
		Encoding: payload.Encoding, Truncated: payload.Truncated, ReadError: payload.ReadError,
	}
	if payload.CapturedSize > 0 {
		view.DownloadURL = fmt.Sprintf("/api/sessions/%s/requests/%d/body/%s", url.PathEscape(sessionID), requestID, side)
	}
	text, decodedEncoding, truncated, err := payloadTextForDisplay(payload, firstHeader(headers, "Content-Encoding"))
	if err != nil {
		view.DisplayError = err.Error()
		return view
	}
	if text != "" || payload.CapturedSize > 0 && decodedEncoding != "" {
		view.Encoding = "utf8"
		view.Data = text
		view.DecodedEncoding = decodedEncoding
		view.DisplayTruncated = truncated
	}
	return view
}

func payloadTextForDisplay(payload capturedPayload, contentEncoding string) (string, string, bool, error) {
	if payload.CapturedSize <= 0 {
		return "", "", false, nil
	}
	content, err := payloadBytes(payload)
	if err != nil {
		return "", "", false, err
	}
	decodedEncoding := ""
	decodedTruncated := false
	encodings := contentEncodings(contentEncoding)
	for index := len(encodings) - 1; index >= 0; index-- {
		encoding := encodings[index]
		if encoding == "identity" {
			continue
		}
		decoded, truncated, err := decodeContentEncoding(content, encoding)
		if err != nil {
			return "", decodedEncoding, false, err
		}
		content = decoded
		decodedTruncated = decodedTruncated || truncated
		if decodedEncoding == "" {
			decodedEncoding = encoding
		} else {
			decodedEncoding += "," + encoding
		}
	}
	if !displayBytesAreText(content) {
		return "", decodedEncoding, false, nil
	}
	text := string(content)
	truncated := len(text) > maxDisplayBodyBytes
	display := truncateUTF8(text, maxDisplayBodyBytes)
	return display, decodedEncoding, decodedTruncated || truncated || len(display) < len(text), nil
}

func contentEncodings(value string) []string {
	parts := strings.Split(value, ",")
	encodings := make([]string, 0, len(parts))
	for _, part := range parts {
		if encoding := strings.ToLower(strings.TrimSpace(part)); encoding != "" {
			encodings = append(encodings, encoding)
		}
	}
	return encodings
}

func decodeContentEncoding(content []byte, encoding string) ([]byte, bool, error) {
	var reader io.ReadCloser
	var err error
	switch encoding {
	case "gzip", "x-gzip":
		reader, err = gzip.NewReader(bytes.NewReader(content))
	case "deflate":
		reader, err = zlib.NewReader(bytes.NewReader(content))
	default:
		return nil, false, fmt.Errorf("暂不支持 %s Body 解码", encoding)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s Body 解码失败: %w", encoding, err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, int64(maxDisplayBodyBytes)+1))
	if err != nil {
		return nil, false, fmt.Errorf("%s Body 解码失败: %w", encoding, err)
	}
	truncated := len(decoded) > maxDisplayBodyBytes
	if len(decoded) > maxDisplayBodyBytes {
		decoded = decoded[:maxDisplayBodyBytes]
		for !utf8.Valid(decoded) && len(decoded) > 0 {
			decoded = decoded[:len(decoded)-1]
		}
	}
	return decoded, truncated, nil
}

func displayBytesAreText(content []byte) bool {
	if !utf8.Valid(content) {
		return false
	}
	if len(content) == 0 {
		return true
	}
	controls := 0
	for _, value := range content {
		if value == 0 {
			return false
		}
		if value < 32 && value != '\t' && value != '\n' && value != '\r' {
			controls++
		}
	}
	return controls <= 8 || float64(controls)/float64(len(content)) <= 0.01
}

func payloadBytes(payload capturedPayload) ([]byte, error) {
	switch payload.Encoding {
	case "", "utf8":
		return []byte(payload.Data), nil
	case "base64":
		return base64.StdEncoding.DecodeString(payload.Data)
	default:
		return nil, fmt.Errorf("未知 Body 编码 %q", payload.Encoding)
	}
}

func (index *webIndex) sessionDirectory(ctx context.Context, sessionID string) (string, error) {
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	var directory string
	err := index.db.QueryRowContext(ctx, `SELECT directory FROM sessions WHERE id = ?`, sessionID).Scan(&directory)
	if errors.Is(err, sql.ErrNoRows) {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	if err := ensurePathWithin(index.root, directory); err != nil {
		return "", err
	}
	return directory, nil
}

func (index *webIndex) moveSessionToTrash(ctx context.Context, sessionID string) (string, error) {
	index.mu.Lock()
	defer index.mu.Unlock()
	directory, err := index.sessionDirectory(ctx, sessionID)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("会话目录不是可移动的普通目录")
	}
	trashRoot := filepath.Join(index.root, ".trash")
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		return "", err
	}
	destination := filepath.Join(trashRoot, sessionID+"-"+time.Now().Format("20060102-150405"))
	if err := ensurePathWithin(trashRoot, destination); err != nil {
		return "", err
	}
	if err := os.Rename(directory, destination); err != nil {
		return "", fmt.Errorf("移动会话到回收目录: %w", err)
	}
	if err := index.deleteIndexedSession(ctx, sessionID); err != nil {
		return destination, fmt.Errorf("会话已移动到 %s，但清理索引失败: %w", destination, err)
	}
	return destination, nil
}

func readJSONLine(reader *bufio.Reader, maximum int64) ([]byte, int64, bool, error) {
	var result []byte
	var consumed int64
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += int64(len(fragment))
		if !oversized && int64(len(result)+len(fragment)) <= maximum {
			result = append(result, fragment...)
		} else if len(fragment) > 0 {
			oversized = true
			result = nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return result, consumed, oversized, err
	}
}

func readLimitedFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("文件超过允许大小")
	}
	return content, nil
}

func regularFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("不是普通文件")
	}
	return info, nil
}

func ensurePathWithin(root, candidate string) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absoluteCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteCandidate)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("路径超出会话目录")
	}
	return nil
}

func validateSessionID(id string) error {
	if id == "" || len(id) > 200 || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return errors.New("会话 ID 无效")
	}
	for _, character := range id {
		if unicode.IsControl(character) {
			return errors.New("会话 ID 不能包含控制字符")
		}
	}
	return nil
}

func buildFTSQuery(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, `"`)
		if field == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(field, `"`, `""`)+`"`+"*")
	}
	return strings.Join(parts, " AND ")
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	truncated := value[:maximum]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
