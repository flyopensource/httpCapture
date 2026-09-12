package main

import (
	"bufio"
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
	"time"
)

func proxifyRecordStart(packages []string, clientIP, deviceName string) error {
	proxyState, err := readProxifyState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("Proxify 尚未启动，请先执行 `httpcapture proxy proxify start`")
		}
		return err
	}
	if !managedProcessMatches(proxyState.PID, proxyState.StartToken) {
		return errors.New("受管 Proxify 当前未运行，请重新执行 `httpcapture proxy proxify start`")
	}
	trafficInfo, err := os.Stat(proxyState.TrafficPath)
	if err != nil {
		return fmt.Errorf("读取 Proxify 实时数据文件: %w", err)
	}
	captureID, err := newCaptureID()
	if err != nil {
		return err
	}
	sessionDir, err := sessionDirectory(captureID)
	if err != nil {
		return err
	}
	state := sessionState{
		CaptureID: captureID, Engine: engineProxify, EngineVersion: proxyState.Version,
		EngineRevision: proxyState.SourceRevision,
		Packages:       packages, DeviceName: deviceName, ClientIP: clientIP,
		ProxyHost: proxyState.Host, ProxyPort: proxyState.Port,
		StartedMS: time.Now().UnixMilli(), Status: "recording", SessionDir: sessionDir,
		TrafficSource: proxyState.TrafficPath, StartOffset: trafficInfo.Size(),
	}
	if err := writeSessionMetadata(state); err != nil {
		return err
	}
	if err := writeState(state); err != nil {
		return err
	}
	fmt.Printf("Proxify 已开始记录，captureId=%s，开始时间(ms)=%d\n目录: %s\n", state.CaptureID, state.StartedMS, state.SessionDir)
	if len(packages) > 1 {
		fmt.Println("提示: 多 App 会话只保存包名集合，不声明单条请求的包名归属。")
	}
	return nil
}

func proxifyRecordStop(state sessionState, outputDirectory, formats string) error {
	requested, err := parseProxifyFormats(formats)
	if err != nil {
		return err
	}
	state.StoppedMS = time.Now().UnixMilli()
	originalSessionDir := state.SessionDir
	outputDirectory = strings.TrimSpace(outputDirectory)
	if outputDirectory == "" {
		outputDirectory = state.SessionDir
	}
	if err := os.MkdirAll(outputDirectory, 0o700); err != nil {
		return fmt.Errorf("创建抓包会话目录: %w", err)
	}
	if err := os.Chmod(outputDirectory, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(state.TrafficSource)
	if err != nil {
		return fmt.Errorf("读取 Proxify 实时数据文件: %w", err)
	}
	trafficPath := filepath.Join(outputDirectory, "traffic.jsonl")
	count, skipped, err := extractCapturedTransactions(
		state.TrafficSource, trafficPath, state.StartOffset, info.Size(),
		state.StartedMS, state.StoppedMS, state.ClientIP,
	)
	if err != nil {
		return err
	}
	if requested["har"] {
		if err := exportCaptureHAR(trafficPath, filepath.Join(outputDirectory, "session.har")); err != nil {
			return fmt.Errorf("导出 HAR: %w", err)
		}
	}
	state.Status = "completed"
	state.SessionDir = outputDirectory
	state.RequestCount = count
	state.SkippedCount = skipped
	if err := writeSessionMetadata(state); err != nil {
		return err
	}
	if filepath.Clean(originalSessionDir) != filepath.Clean(outputDirectory) {
		if err := writeSessionMetadataTo(originalSessionDir, state); err != nil {
			return err
		}
	}
	if err := clearState(); err != nil {
		return err
	}
	fmt.Printf("Proxify 抓包会话已停止，结束时间(ms)=%d\n请求数: %d\n导出目录: %s\n", state.StoppedMS, count, outputDirectory)
	if skipped > 0 {
		fmt.Printf("警告: 跳过 %d 条不完整或不匹配的实时记录\n", skipped)
	}
	if !requested["jsonl"] {
		fmt.Println("说明: traffic.jsonl 是会话原始主数据，因此即使未指定 jsonl 也会保留。")
	}
	return nil
}

func parseProxifyFormats(value string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, format := range strings.Split(value, ",") {
		format = strings.ToLower(strings.TrimSpace(format))
		if format == "" {
			continue
		}
		if format != "jsonl" && format != "har" {
			return nil, fmt.Errorf("Proxify 不支持导出格式 %q；仅支持 jsonl、har", format)
		}
		result[format] = true
	}
	if len(result) == 0 {
		return nil, errors.New("至少指定一种导出格式")
	}
	return result, nil
}

func extractCapturedTransactions(sourcePath, destinationPath string, startOffset, endOffset, startMS, endMS int64, clientIP string) (int, int, error) {
	if startOffset < 0 || endOffset < startOffset {
		return 0, 0, errors.New("实时抓包文件偏移无效")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return 0, 0, err
	}
	defer source.Close()
	if _, err := source.Seek(startOffset, io.SeekStart); err != nil {
		return 0, 0, err
	}
	temporaryPath := destinationPath + ".partial"
	destination, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, err
	}
	success := false
	defer func() {
		_ = destination.Close()
		if !success {
			_ = os.Remove(temporaryPath)
		}
	}()

	reader := bufio.NewReaderSize(io.LimitReader(source, endOffset-startOffset), 64*1024)
	count := 0
	skipped := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			var transaction capturedTransaction
			if err := json.Unmarshal([]byte(trimmed), &transaction); err != nil || transaction.SchemaVersion != captureSchemaVersion {
				skipped++
			} else if transaction.TimestampMillis < startMS || transaction.TimestampMillis > endMS || clientIP != "" && !clientAddressMatches(transaction.ClientAddress, clientIP) {
				skipped++
			} else {
				if _, err := destination.WriteString(trimmed + "\n"); err != nil {
					return 0, 0, err
				}
				count++
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, 0, readErr
		}
	}
	if err := destination.Sync(); err != nil {
		return 0, 0, err
	}
	if err := destination.Close(); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		return 0, 0, err
	}
	if err := os.Chmod(destinationPath, 0o600); err != nil {
		return 0, 0, err
	}
	success = true
	return count, skipped, nil
}

type harNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func exportCaptureHAR(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temporaryPath := destinationPath + ".partial"
	destination, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = destination.Close()
		if !success {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := io.WriteString(destination, `{"log":{"version":"1.2","creator":{"name":"httpcapture","version":"`+version+`"},"entries":[`); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(source, 64*1024)
	first := true
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var transaction capturedTransaction
			if err := json.Unmarshal(line, &transaction); err != nil {
				return fmt.Errorf("解析 JSONL: %w", err)
			}
			entry, err := json.Marshal(transactionToHAR(transaction))
			if err != nil {
				return err
			}
			if !first {
				if _, err := destination.WriteString(","); err != nil {
					return err
				}
			}
			first = false
			if _, err := destination.Write(entry); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if _, err := io.WriteString(destination, `]}}`); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		return err
	}
	if err := os.Chmod(destinationPath, 0o600); err != nil {
		return err
	}
	success = true
	return nil
}

func transactionToHAR(transaction capturedTransaction) map[string]any {
	requestBodySize := payloadSize(transaction.Request.Body)
	responseBodySize := payloadSize(transaction.Response.Body)
	request := map[string]any{
		"method": transaction.Request.Method, "url": transaction.Request.URL,
		"httpVersion": transaction.Request.HTTPVersion,
		"headers":     headerNameValues(transaction.Request.Headers),
		"queryString": queryNameValues(transaction.Request.URL),
		"cookies":     []any{}, "headersSize": -1, "bodySize": requestBodySize,
	}
	if transaction.Request.Body.CapturedSize > 0 {
		postData := map[string]any{
			"mimeType": firstHeader(transaction.Request.Headers, "Content-Type"),
			"text":     transaction.Request.Body.Data,
		}
		if transaction.Request.Body.Encoding == "base64" {
			postData["comment"] = "body is base64 encoded"
		}
		request["postData"] = postData
	}
	content := map[string]any{
		"size":     responseBodySize,
		"mimeType": firstHeader(transaction.Response.Headers, "Content-Type"),
		"text":     transaction.Response.Body.Data,
	}
	if transaction.Response.Body.Encoding == "base64" {
		content["encoding"] = "base64"
	}
	if transaction.Response.Body.Truncated {
		content["comment"] = "body truncated by httpcapture capture limit"
	}
	statusText := transaction.Response.Status
	statusPrefix := strconv.Itoa(transaction.Response.StatusCode) + " "
	statusText = strings.TrimPrefix(statusText, statusPrefix)
	return map[string]any{
		"startedDateTime": transaction.Timestamp,
		"time":            transaction.DurationMillis,
		"request":         request,
		"response": map[string]any{
			"status": transaction.Response.StatusCode, "statusText": statusText,
			"httpVersion": transaction.Response.HTTPVersion,
			"headers":     headerNameValues(transaction.Response.Headers), "cookies": []any{},
			"content": content, "redirectURL": firstHeader(transaction.Response.Headers, "Location"),
			"headersSize": -1, "bodySize": responseBodySize,
		},
		"cache":   map[string]any{},
		"timings": map[string]any{"send": 0, "wait": transaction.DurationMillis, "receive": 0},
	}
}

func headerNameValues(headers map[string][]string) []harNameValue {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]harNameValue, 0, len(headers))
	for _, key := range keys {
		for _, value := range headers[key] {
			result = append(result, harNameValue{Name: key, Value: value})
		}
	}
	return result
}

func queryNameValues(rawURL string) []harNameValue {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return []harNameValue{}
	}
	keys := make([]string, 0, len(parsed.Query()))
	for key := range parsed.Query() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]harNameValue, 0)
	for _, key := range keys {
		for _, value := range parsed.Query()[key] {
			result = append(result, harNameValue{Name: key, Value: value})
		}
	}
	return result
}

func firstHeader(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func payloadSize(payload capturedPayload) int64 {
	if payload.DeclaredSize >= 0 {
		return payload.DeclaredSize
	}
	return int64(payload.CapturedSize)
}
