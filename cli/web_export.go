package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const maxWebExportSpecBytes = 1 << 20

type webExportSessionFilter struct {
	Engine  string `json:"engine"`
	Package string `json:"package"`
	Device  string `json:"device"`
	FromMS  int64  `json:"fromMs"`
	ToMS    int64  `json:"toMs"`
}

type webExportSelection struct {
	SessionID string `json:"sessionId"`
	ID        int64  `json:"id"`
}

type webExportSpec struct {
	Scope         string                 `json:"scope"`
	SessionID     string                 `json:"sessionId,omitempty"`
	SessionFilter webExportSessionFilter `json:"sessionFilter"`
	RequestFilter requestQuery           `json:"requestFilter"`
	SessionIDs    []string               `json:"sessionIds,omitempty"`
	RequestIDs    []webExportSelection   `json:"requestIds,omitempty"`
}

type webExportFile struct {
	Path            string `json:"path"`
	SHA256          string `json:"sha256"`
	ExportedRecords int    `json:"exportedRecords"`
	SkippedRecords  int    `json:"skippedRecords"`
}

type webExportSession struct {
	ID                string        `json:"id"`
	Engine            string        `json:"engine"`
	Packages          []string      `json:"packages"`
	TrafficSizeAtScan int64         `json:"trafficSizeAtScan"`
	File              webExportFile `json:"file"`
}

type webExportManifest struct {
	Version                     int                    `json:"version"`
	ExportedAtMillis            int64                  `json:"exportedAtMillis"`
	Scope                       string                 `json:"scope"`
	SessionID                   string                 `json:"sessionId,omitempty"`
	SessionFilter               webExportSessionFilter `json:"sessionFilter"`
	RequestFilter               requestQuery           `json:"requestFilter"`
	RequestTimeSource           string                 `json:"requestTimeSource"`
	ResponseCompletedTimeSource string                 `json:"responseCompletedTimeSource"`
	Status                      string                 `json:"status"`
	ExportedRecords             int                    `json:"exportedRecords"`
	SkippedRecords              int                    `json:"skippedRecords"`
	Sessions                    []webExportSession     `json:"sessions"`
	Warnings                    []string               `json:"warnings,omitempty"`
}

func (app *webApplication) handleBatchExport(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, maxWebExportSpecBytes)
	if err := request.ParseForm(); err != nil {
		writeAPIError(response, http.StatusBadRequest, "导出参数过大或格式无效")
		return
	}
	if request.Header.Get("X-HttpCapture-Token") != app.token &&
		request.Form.Get("token") != app.token {
		writeAPIError(response, http.StatusForbidden, "操作令牌无效，请刷新页面")
		return
	}
	var spec webExportSpec
	if err := json.Unmarshal([]byte(request.Form.Get("spec")), &spec); err != nil {
		writeAPIError(response, http.StatusBadRequest, "导出配置 JSON 无效")
		return
	}
	if err := validateWebExportSpec(spec); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := app.index.rescan(request.Context()); err != nil {
		writeAPIError(response, http.StatusInternalServerError, "导出前扫描会话失败: "+err.Error())
		return
	}
	app.index.mu.RLock()
	defer app.index.mu.RUnlock()
	all, err := app.index.listSessions(
		request.Context(), spec.SessionFilter.Engine, spec.SessionFilter.Package,
		spec.SessionFilter.Device, spec.SessionFilter.FromMS, spec.SessionFilter.ToMS,
	)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	eligible, selections, err := selectWebExportSessions(all, spec)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Disposition", downloadDisposition(
		"httpcapture-export-"+time.Now().Format("20060102-150405")+".zip",
	))
	writer := zip.NewWriter(response)
	manifest := webExportManifest{
		Version: 1, ExportedAtMillis: time.Now().UnixMilli(),
		Scope: spec.Scope, SessionID: spec.SessionID,
		SessionFilter: spec.SessionFilter, RequestFilter: spec.RequestFilter,
		RequestTimeSource:           "traffic.jsonl timestampMillis (Unix milliseconds)",
		ResponseCompletedTimeSource: "unavailable; durationMillis is measured duration, not an independently sampled completion timestamp",
		Status:                      "complete",
		Sessions:                    make([]webExportSession, 0, len(eligible)),
	}
	for _, session := range eligible {
		file, err := exportWebSession(writer, app.index, request, session, spec, selections[session.ID], &manifest)
		if err != nil {
			manifest.Status = "failed"
			addWebExportWarning(&manifest, fmt.Sprintf("会话 %s 导出中断: %v", session.ID, err))
			break
		}
		manifest.Sessions = append(manifest.Sessions, webExportSession{
			ID: session.ID, Engine: session.Engine, Packages: session.Packages,
			TrafficSizeAtScan: session.TrafficSize, File: file,
		})
		manifest.ExportedRecords += file.ExportedRecords
		manifest.SkippedRecords += file.SkippedRecords
	}
	if manifest.SkippedRecords > 0 && manifest.Status == "complete" {
		manifest.Status = "partial"
	}
	entry, err := writer.Create("manifest.json")
	if err == nil {
		err = json.NewEncoder(entry).Encode(manifest)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "写入导出清单失败:", err)
	}
	if err := writer.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "关闭导出 ZIP 失败:", err)
	}
}

func validateWebExportSpec(spec webExportSpec) error {
	if spec.Scope != "all" && spec.Scope != "filtered" && spec.Scope != "selected" {
		return errors.New("导出范围必须是 all、filtered 或 selected")
	}
	if spec.SessionID != "" {
		if err := validateSessionID(spec.SessionID); err != nil {
			return err
		}
	}
	if len(spec.SessionIDs) > 10000 || len(spec.RequestIDs) > 10000 {
		return errors.New("手选项超过 10000，请改用当前条件导出")
	}
	for _, id := range spec.SessionIDs {
		if err := validateSessionID(id); err != nil {
			return err
		}
	}
	for _, selection := range spec.RequestIDs {
		if err := validateSessionID(selection.SessionID); err != nil || selection.ID <= 0 {
			return errors.New("选择的请求 ID 无效")
		}
	}
	if spec.Scope == "selected" && len(spec.SessionIDs)+len(spec.RequestIDs) == 0 {
		return errors.New("请先选择要导出的会话或请求")
	}
	if spec.SessionFilter.FromMS < 0 || spec.SessionFilter.ToMS < 0 {
		return errors.New("会话时间筛选无效")
	}
	return nil
}

func selectWebExportSessions(all []indexedSession, spec webExportSpec) (
	[]indexedSession, map[string][]int64, error,
) {
	selectedSessions := make(map[string]bool, len(spec.SessionIDs))
	for _, id := range spec.SessionIDs {
		selectedSessions[id] = true
	}
	selectedRequests := make(map[string][]int64)
	seenRequests := make(map[string]map[int64]bool)
	for _, selection := range spec.RequestIDs {
		if seenRequests[selection.SessionID] == nil {
			seenRequests[selection.SessionID] = make(map[int64]bool)
		}
		if !seenRequests[selection.SessionID][selection.ID] {
			seenRequests[selection.SessionID][selection.ID] = true
			selectedRequests[selection.SessionID] = append(selectedRequests[selection.SessionID], selection.ID)
		}
	}
	for _, ids := range selectedRequests {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	result := make([]indexedSession, 0)
	found := make(map[string]bool)
	for _, session := range all {
		include := false
		switch spec.Scope {
		case "all", "filtered":
			include = spec.SessionID == "" || session.ID == spec.SessionID
		case "selected":
			include = selectedSessions[session.ID] || len(selectedRequests[session.ID]) > 0
		}
		if !include {
			continue
		}
		if session.Engine != engineProxify {
			if spec.SessionID == session.ID || selectedSessions[session.ID] || len(selectedRequests[session.ID]) > 0 {
				return nil, nil, fmt.Errorf("会话 %s 没有 Proxify 逐请求索引；只能下载已有原始文件", session.ID)
			}
			continue
		}
		result = append(result, session)
		found[session.ID] = true
	}
	if spec.SessionID != "" && !found[spec.SessionID] {
		return nil, nil, errors.New("未找到可导出的 Proxify 会话")
	}
	for id := range selectedSessions {
		if !found[id] {
			return nil, nil, fmt.Errorf("选中会话 %s 不在当前可导出范围内", id)
		}
	}
	for id := range selectedRequests {
		if !found[id] {
			return nil, nil, fmt.Errorf("选中请求所在会话 %s 不在当前可导出范围内", id)
		}
	}
	if len(result) == 0 {
		return nil, nil, errors.New("当前范围没有可导出的 Proxify 会话")
	}
	return result, selectedRequests, nil
}

func exportWebSession(
	writer *zip.Writer, index *webIndex, request *http.Request,
	session indexedSession, spec webExportSpec, selectedIDs []int64, manifest *webExportManifest,
) (webExportFile, error) {
	path := "sessions/" + session.ID + "/traffic.jsonl"
	entry, err := writer.Create(path)
	if err != nil {
		return webExportFile{}, err
	}
	digest := sha256.New()
	destination := io.MultiWriter(entry, digest)
	result := webExportFile{Path: path}
	if session.IndexError != "" {
		manifest.Status = "partial"
		addWebExportWarning(manifest, fmt.Sprintf("会话 %s 索引告警: %s", session.ID, session.IndexError))
		var skipped int
		if _, err := fmt.Sscanf(session.IndexError, "跳过 %d 条", &skipped); err == nil && skipped > 0 {
			result.SkippedRecords += skipped
		}
	}
	writeOne := func(id int64) error {
		if err := request.Context().Err(); err != nil {
			return err
		}
		transaction, err := index.requestTransaction(request.Context(), session.ID, id)
		if err != nil {
			result.SkippedRecords++
			addWebExportWarning(manifest, fmt.Sprintf("会话 %s 请求 %d 原始记录不可读: %v", session.ID, id, err))
			return nil
		}
		content, err := json.Marshal(transaction)
		if err != nil {
			result.SkippedRecords++
			addWebExportWarning(manifest, fmt.Sprintf("会话 %s 请求 %d 编码失败: %v", session.ID, id, err))
			return nil
		}
		content = append(content, '\n')
		if _, err := destination.Write(content); err != nil {
			return err
		}
		result.ExportedRecords++
		if transaction.TimestampMillis <= 0 || transaction.DurationMillis < 0 {
			manifest.Status = "partial"
			addWebExportWarning(manifest, fmt.Sprintf("会话 %s 请求 %d 时间字段缺失或无效", session.ID, id))
		}
		if incompleteCapturedPayload(transaction.Request.Body) ||
			incompleteCapturedPayload(transaction.Response.Body) {
			manifest.Status = "partial"
			addWebExportWarning(manifest, fmt.Sprintf("会话 %s 请求 %d Body 已截断或读取失败", session.ID, id))
		}
		return nil
	}
	if spec.Scope == "selected" && !containsWebExportSession(spec.SessionIDs, session.ID) {
		for _, id := range selectedIDs {
			if err := writeOne(id); err != nil {
				return result, err
			}
		}
	} else {
		filter := spec.RequestFilter
		filter.SessionID = session.ID
		filter.PageSize = 500
		if spec.Scope == "all" || spec.Scope == "selected" {
			filter = requestQuery{SessionID: session.ID, PageSize: 500, SortAscending: true}
		}
		for page := 1; ; page++ {
			filter.Page = page
			batch, err := index.listRequests(request.Context(), filter)
			if err != nil {
				return result, err
			}
			for _, item := range batch.Items {
				if err := writeOne(item.ID); err != nil {
					return result, err
				}
			}
			if page*filter.PageSize >= batch.Total {
				break
			}
		}
	}
	result.SHA256 = strings.ToUpper(hex.EncodeToString(digest.Sum(nil)))
	return result, nil
}

func containsWebExportSession(ids []string, id string) bool {
	for _, selected := range ids {
		if selected == id {
			return true
		}
	}
	return false
}

func addWebExportWarning(manifest *webExportManifest, message string) {
	if len(manifest.Warnings) < 100 {
		manifest.Warnings = append(manifest.Warnings, message)
	}
}

func incompleteCapturedPayload(payload capturedPayload) bool {
	if payload.Truncated || payload.ReadError != "" ||
		payload.DeclaredSize > int64(payload.CapturedSize) ||
		payload.CapturedSize > 0 && payload.Data == "" {
		return true
	}
	switch payload.Encoding {
	case "", "utf8":
		return len([]byte(payload.Data)) != payload.CapturedSize
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(payload.Data)
		return err != nil || len(decoded) != payload.CapturedSize
	default:
		return true
	}
}
