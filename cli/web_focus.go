package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	webFocusDirectoryName = ".web"
	webFocusFileName      = "focus-rules.json"
	maxFocusRules         = 100
	maxFocusRulePattern   = 512
)

type focusRule struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"`
	Method    string `json:"method,omitempty"`
	Pattern   string `json:"pattern"`
	Enabled   bool   `json:"enabled"`
	CreatedMS int64  `json:"createdAt,omitempty"`
	UpdatedMS int64  `json:"updatedAt,omitempty"`
}

type focusRulesDocument struct {
	Rules []focusRule `json:"rules"`
}

func (app *webApplication) handleFocusRules(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		document, err := app.readFocusRules()
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(response, http.StatusOK, document)
	case http.MethodPut:
		if !app.authorizeMutation(response, request) {
			return
		}
		defer request.Body.Close()
		var document focusRulesDocument
		decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 128*1024))
		if err := decoder.Decode(&document); err != nil {
			writeAPIError(response, http.StatusBadRequest, "Focus 规则 JSON 无效: "+err.Error())
			return
		}
		normalized, err := normalizeFocusRules(document.Rules)
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, err.Error())
			return
		}
		document = focusRulesDocument{Rules: normalized}
		if err := app.writeFocusRules(document); err != nil {
			writeAPIError(response, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(response, http.StatusOK, document)
	default:
		response.Header().Set("Allow", "GET, PUT")
		writeAPIError(response, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (app *webApplication) readFocusRules() (focusRulesDocument, error) {
	path, err := app.focusRulesPath(false)
	if err != nil {
		return focusRulesDocument{}, err
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return focusRulesDocument{Rules: []focusRule{}}, nil
	}
	if err != nil {
		return focusRulesDocument{}, err
	}
	var document focusRulesDocument
	if err := json.Unmarshal(content, &document); err != nil {
		return focusRulesDocument{}, err
	}
	rules, err := normalizeFocusRules(document.Rules)
	if err != nil {
		return focusRulesDocument{}, err
	}
	return focusRulesDocument{Rules: rules}, nil
}

func (app *webApplication) writeFocusRules(document focusRulesDocument) error {
	path, err := app.focusRulesPath(true)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return secureWrite(path, append(content, '\n'))
}

func (app *webApplication) focusRulesPath(create bool) (string, error) {
	directory := filepath.Join(app.index.root, webFocusDirectoryName)
	if err := ensurePathWithin(app.index.root, directory); err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return "", err
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return "", err
		}
	}
	path := filepath.Join(directory, webFocusFileName)
	if err := ensurePathWithin(app.index.root, path); err != nil {
		return "", err
	}
	return path, nil
}

func normalizeFocusRules(rules []focusRule) ([]focusRule, error) {
	if len(rules) > maxFocusRules {
		return nil, errors.New("Focus 规则最多 100 条")
	}
	now := time.Now().UnixMilli()
	normalized := make([]focusRule, 0, len(rules))
	seen := map[string]bool{}
	for _, rule := range rules {
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			rule.ID = newFocusRuleID()
		}
		if seen[rule.ID] {
			return nil, errors.New("Focus 规则 ID 重复")
		}
		seen[rule.ID] = true
		rule.Type = strings.TrimSpace(rule.Type)
		if rule.Type == "" {
			rule.Type = "url_contains"
		}
		if !validFocusRuleType(rule.Type) {
			return nil, errors.New("Focus 规则类型无效")
		}
		rule.Method = strings.ToUpper(strings.TrimSpace(rule.Method))
		if rule.Type == "method_url_contains" && rule.Method != "" && !validHTTPMethod(rule.Method) {
			return nil, errors.New("Focus Method 无效")
		}
		rule.Pattern = strings.TrimSpace(rule.Pattern)
		if rule.Pattern == "" {
			return nil, errors.New("Focus 规则内容不能为空")
		}
		if len(rule.Pattern) > maxFocusRulePattern {
			return nil, errors.New("Focus 规则内容过长")
		}
		rule.Name = strings.TrimSpace(rule.Name)
		if len(rule.Name) > 100 {
			return nil, errors.New("Focus 规则名称过长")
		}
		if rule.CreatedMS <= 0 {
			rule.CreatedMS = now
		}
		if rule.UpdatedMS <= 0 {
			rule.UpdatedMS = now
		}
		normalized = append(normalized, rule)
	}
	return normalized, nil
}

func validFocusRuleType(value string) bool {
	switch value {
	case "url_contains", "host_contains", "path_contains", "method_url_contains":
		return true
	default:
		return false
	}
}

func validHTTPMethod(value string) bool {
	switch value {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func newFocusRuleID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(bytes[:])
}
