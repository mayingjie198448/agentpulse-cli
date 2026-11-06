package autostart

import (
	"fmt"
	"os"
	"runtime"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

// ServiceLabel is the launchd service label for AgentPulse daemon.
const ServiceLabel = "com.agentpulse.daemon"

// PlistFileName is the filename of the LaunchAgent property list.
const PlistFileName = "com.agentpulse.daemon.plist"

// StatusInfo represents the runtime and configuration status of login autostart.
type StatusInfo struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Detail    string `json:"detail"`
	PlistPath string `json:"plist_path,omitempty"`
}

// PublishOffline publishes an explicit offline status retained message for machineID
// to the MQTT broker so that an old node ID does not linger ONLINE after a machine_id change.
func PublishOffline(brokerCfg config.BrokerConfig, machineID string) error {
	if brokerCfg.URL == "" || machineID == "" {
		return nil
	}
	mqttCfg := mqtt.DefaultConfig()
	mqttCfg.BrokerURL = brokerCfg.URL
	mqttCfg.Username = brokerCfg.Username
	mqttCfg.Password = brokerCfg.Password
	mqttCfg.TLSEnable = brokerCfg.TLS
	mqttCfg.CACertFile = brokerCfg.CACertFile
	mqttCfg.ClientCertFile = brokerCfg.ClientCertFile
	mqttCfg.ClientKeyFile = brokerCfg.ClientKeyFile
	mqttCfg.InsecureSkipVerify = brokerCfg.InsecureSkipVerify
	mqttCfg.ServerName = brokerCfg.ServerName
	mqttCfg.ClientID = fmt.Sprintf("agentpulse-offline-%s-%d", machineID, time.Now().UnixNano()%100000)
	mqttCfg.Timeout = 5 * time.Second

	opts := paho.NewClientOptions()
	if err := mqtt.ApplyClientOptions(opts, mqttCfg); err != nil {
		return err
	}
	client := paho.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("MQTT connection timed out to %s", brokerCfg.URL)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT connection failed to %s: %w", brokerCfg.URL, err)
	}
	defer client.Disconnect(100)

	topic := fmt.Sprintf("agentpulse/v1/machines/%s/status", machineID)
	payload := fmt.Sprintf(`{"id":%q,"name":%q,"online":false,"last_heartbeat":%q,"platform":%q,"job_count":0}`,
		machineID, machineID, time.Now().UTC().Format(time.RFC3339), fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))

	pubToken := client.Publish(topic, 1, true, []byte(payload))
	if !pubToken.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("publish offline timed out on %s", topic)
	}
	if err := pubToken.Error(); err != nil {
		return fmt.Errorf("publish offline failed on %s: %w", topic, err)
	}
	return nil
}

// resolveExecutable returns the absolute path to the current agentpulse binary.
func resolveExecutable(customPath string) (string, error) {
	if customPath != "" {
		return customPath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine executable path: %w", err)
	}
	return exe, nil
}
