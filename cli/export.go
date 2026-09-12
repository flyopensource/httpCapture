package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type exportFilter struct {
	fromMS   int64
	toMS     int64
	clientIP string
}

func exportCommand(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	input := fs.String("input", "", "Charles XML 或 HAR 文件")
	output := fs.String("output", "", "过滤后的输出文件")
	fromMS := fs.Int64("from-ms", 0, "包含的最早 startTimeMillis")
	toMS := fs.Int64("to-ms", 0, "包含的最晚 startTimeMillis")
	clientIP := fs.String("client-ip", "", "Charles XML clientAddress")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *output == "" {
		return errors.New("--input 和 --output 必填")
	}
	filter := exportFilter{fromMS: *fromMS, toMS: *toMS, clientIP: *clientIP}
	switch strings.ToLower(filepath.Ext(*input)) {
	case ".xml":
		return filterXML(*input, *output, filter)
	case ".har":
		if *clientIP != "" {
			return errors.New("HAR 不含可靠的 clientAddress；按客户端 IP 过滤请导出 XML")
		}
		return filterHAR(*input, *output, filter)
	default:
		return errors.New("仅支持 .xml 和 .har；.chls 请先通过 Charles 导出")
	}
}

func filterXML(input, output string, filter exportFilter) error {
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer in.Close()
	var result bytes.Buffer
	decoder := xml.NewDecoder(bufio.NewReader(in))
	encoder := xml.NewEncoder(&result)
	kept := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		start, isStart := token.(xml.StartElement)
		if !isStart || start.Name.Local != "transaction" {
			if err := encoder.EncodeToken(token); err != nil {
				return err
			}
			continue
		}
		var block bytes.Buffer
		blockEncoder := xml.NewEncoder(&block)
		if err := blockEncoder.EncodeToken(start); err != nil {
			return err
		}
		depth := 1
		for depth > 0 {
			nested, err := decoder.Token()
			if err != nil {
				return err
			}
			if nestedStart, ok := nested.(xml.StartElement); ok && nestedStart.Name.Local == "transaction" {
				depth++
			}
			if nestedEnd, ok := nested.(xml.EndElement); ok && nestedEnd.Name.Local == "transaction" {
				depth--
			}
			if err := blockEncoder.EncodeToken(nested); err != nil {
				return err
			}
		}
		_ = blockEncoder.Flush()
		if transactionMatches(start, filter) {
			if err := encoder.Flush(); err != nil {
				return err
			}
			result.Write(block.Bytes())
			kept++
		}
	}
	if err := encoder.Flush(); err != nil {
		return err
	}
	if err := secureWrite(output, result.Bytes()); err != nil {
		return err
	}
	fmt.Printf("XML 过滤完成，保留 %d 条请求: %s\n", kept, output)
	return nil
}

func transactionMatches(start xml.StartElement, filter exportFilter) bool {
	var startMS int64
	var client string
	for _, attribute := range start.Attr {
		switch attribute.Name.Local {
		case "startTimeMillis":
			startMS, _ = strconv.ParseInt(attribute.Value, 10, 64)
		case "clientAddress":
			client = attribute.Value
		}
	}
	if filter.fromMS > 0 && startMS < filter.fromMS {
		return false
	}
	if filter.toMS > 0 && startMS > filter.toMS {
		return false
	}
	if filter.clientIP != "" && !clientAddressMatches(client, filter.clientIP) {
		return false
	}
	return true
}

func clientAddressMatches(value, expected string) bool {
	return value == expected || strings.HasPrefix(value, expected+":") || strings.HasPrefix(value, "/"+expected+":")
}

func filterHAR(input, output string, filter exportFilter) error {
	content, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		return err
	}
	log, ok := document["log"].(map[string]any)
	if !ok {
		return errors.New("HAR 缺少 log")
	}
	entries, ok := log["entries"].([]any)
	if !ok {
		return errors.New("HAR 缺少 log.entries")
	}
	filtered := make([]any, 0, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		started, _ := entry["startedDateTime"].(string)
		parsed, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			continue
		}
		millis := parsed.UnixMilli()
		if filter.fromMS > 0 && millis < filter.fromMS || filter.toMS > 0 && millis > filter.toMS {
			continue
		}
		filtered = append(filtered, entry)
	}
	log["entries"] = filtered
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := secureWrite(output, encoded); err != nil {
		return err
	}
	fmt.Printf("HAR 过滤完成，保留 %d 条请求: %s\n", len(filtered), output)
	return nil
}
