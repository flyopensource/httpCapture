package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
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
	RevokedMS   int64  `json:"revokedMs,omitempty"`
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
	for index, device := range registry.Devices {
		if device.Revoked {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(device.TokenSHA256), []byte(digest)) == 1 {
			registry.Devices[index].LastSeenMS = time.Now().UnixMilli()
			if err := saveControlDevices(registry); err != nil {
				return controlDevice{}, false, err
			}
			device.LastSeenMS = registry.Devices[index].LastSeenMS
			return device, true, nil
		}
	}
	return controlDevice{}, false, nil
}

func controlTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}

func controlCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("control 用法: devices 或 revoke")
	}
	switch args[0] {
	case "devices":
		return controlDevicesCommand(args[1:], os.Stdout)
	case "revoke":
		return controlRevokeCommand(args[1:], os.Stdout)
	default:
		return fmt.Errorf("未知 control 子命令 %q", args[0])
	}
}

func controlDevicesCommand(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("control devices", flag.ContinueOnError)
	showAll := flags.Bool("all", false, "显示已撤销设备")
	asJSON := flags.Bool("json", false, "以 JSON 输出；不包含 token 摘要")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("control devices 不接受位置参数")
	}
	registry, err := loadControlDevices()
	if err != nil {
		return err
	}
	devices := make([]controlDeviceView, 0, len(registry.Devices))
	for _, device := range registry.Devices {
		if device.Revoked && !*showAll {
			continue
		}
		devices = append(devices, viewControlDevice(device))
	}
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			Devices []controlDeviceView `json:"devices"`
		}{Devices: devices})
	}
	if len(devices) == 0 {
		fmt.Fprintln(output, "没有已配对的控制设备")
		return nil
	}
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "DEVICE ID\tPROFILE ID\tNAME\tENGINE\tCREATED\tLAST SEEN\tSTATUS")
	for _, device := range devices {
		status := "active"
		if device.Revoked {
			status = "revoked"
		}
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			device.DeviceID,
			device.ProfileID,
			emptyDash(device.DeviceName),
			device.Engine,
			formatControlTime(device.CreatedMS),
			formatControlTime(device.LastSeenMS),
			status,
		)
	}
	return writer.Flush()
}

func controlRevokeCommand(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("control revoke", flag.ContinueOnError)
	deviceID := flags.String("device-id", "", "要撤销的 deviceId")
	profileID := flags.String("profile-id", "", "要撤销的 profileId")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("control revoke 不接受位置参数")
	}
	device, err := revokeControlDevice(*deviceID, *profileID)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "已撤销设备 %s（profile %s）\n", device.DeviceID, device.ProfileID)
	return nil
}

func revokeControlDevice(deviceID, profileID string) (controlDevice, error) {
	deviceID = strings.TrimSpace(deviceID)
	profileID = strings.TrimSpace(profileID)
	if (deviceID == "") == (profileID == "") {
		return controlDevice{}, errors.New("请且仅请指定 --device-id 或 --profile-id")
	}
	registry, err := loadControlDevices()
	if err != nil {
		return controlDevice{}, err
	}
	for index, device := range registry.Devices {
		matched := deviceID != "" && device.DeviceID == deviceID || profileID != "" && device.ProfileID == profileID
		if !matched {
			continue
		}
		if device.Revoked {
			return device, nil
		}
		registry.Devices[index].Revoked = true
		registry.Devices[index].RevokedMS = time.Now().UnixMilli()
		if err := saveControlDevices(registry); err != nil {
			return controlDevice{}, err
		}
		return registry.Devices[index], nil
	}
	return controlDevice{}, errors.New("未找到匹配的控制设备")
}

type controlDeviceView struct {
	ProfileID  string `json:"profileId"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName,omitempty"`
	Engine     string `json:"engine"`
	CreatedMS  int64  `json:"createdMs"`
	LastSeenMS int64  `json:"lastSeenMs,omitempty"`
	Revoked    bool   `json:"revoked,omitempty"`
	RevokedMS  int64  `json:"revokedMs,omitempty"`
}

func viewControlDevice(device controlDevice) controlDeviceView {
	return controlDeviceView{
		ProfileID:  device.ProfileID,
		DeviceID:   device.DeviceID,
		DeviceName: device.DeviceName,
		Engine:     device.Engine,
		CreatedMS:  device.CreatedMS,
		LastSeenMS: device.LastSeenMS,
		Revoked:    device.Revoked,
		RevokedMS:  device.RevokedMS,
	}
}

func formatControlTime(value int64) string {
	if value <= 0 {
		return "-"
	}
	return time.UnixMilli(value).Format(time.RFC3339)
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
