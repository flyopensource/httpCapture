package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const controlDevicesFile = "control-devices.json"

type controlDevice struct {
	ProfileID   string `json:"profileId"`
	DeviceID    string `json:"deviceId"`
	DeviceName  string `json:"deviceName"`
	TokenSHA256 string `json:"tokenSha256"`
	Engine      string `json:"engine"`
	CreatedMS   int64  `json:"createdMs"`
	LastSeenMS  int64  `json:"lastSeenMs,omitempty"`
	Revoked     bool   `json:"revoked,omitempty"`
}

type controlDeviceRegistry struct {
	Devices []controlDevice `json:"devices"`
}

func controlDevicesPath() (string, error) {
	directory, err := configDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, controlDevicesFile), nil
}

func loadControlDevices() (controlDeviceRegistry, error) {
	path, err := controlDevicesPath()
	if err != nil {
		return controlDeviceRegistry{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return controlDeviceRegistry{}, nil
		}
		return controlDeviceRegistry{}, err
	}
	var registry controlDeviceRegistry
	if err := json.Unmarshal(content, &registry); err != nil {
		return controlDeviceRegistry{}, fmt.Errorf("读取控制设备列表: %w", err)
	}
	return registry, nil
}

func saveControlDevices(registry controlDeviceRegistry) error {
	path, err := controlDevicesPath()
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return secureWrite(path, content)
}

func createControlDevice(deviceName, engine string) (controlDevice, string, error) {
	deviceID, err := randomIdentifier(12)
	if err != nil {
		return controlDevice{}, "", err
	}
	profileID, err := randomIdentifier(12)
	if err != nil {
		return controlDevice{}, "", err
	}
	token, err := randomIdentifier(32)
	if err != nil {
		return controlDevice{}, "", err
	}
	registry, err := loadControlDevices()
	if err != nil {
		return controlDevice{}, "", err
	}
	now := time.Now().UnixMilli()
	device := controlDevice{
		ProfileID:   "profile-" + profileID,
		DeviceID:    "device-" + deviceID,
		DeviceName:  strings.TrimSpace(deviceName),
		TokenSHA256: controlTokenDigest(token),
		Engine:      strings.ToLower(strings.TrimSpace(engine)),
		CreatedMS:   now,
	}
	registry.Devices = append(registry.Devices, device)
	if err := saveControlDevices(registry); err != nil {
		return controlDevice{}, "", err
	}
	return device, token, nil
}

func findControlDeviceByToken(token string) (controlDevice, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return controlDevice{}, false, nil
	}
	registry, err := loadControlDevices()
	if err != nil {
		return controlDevice{}, false, err
	}
	digest := controlTokenDigest(token)
	for _, device := range registry.Devices {
		if device.Revoked {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(device.TokenSHA256), []byte(digest)) == 1 {
			return device, true, nil
		}
	}
	return controlDevice{}, false, nil
}

func controlTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}
