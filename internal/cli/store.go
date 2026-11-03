package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/mock"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/spf13/cobra"
)

func registerStoreFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("config", "", "指定配置文件路径 (默认: ~/.config/agentpulse/config.yaml)")
	cmd.PersistentFlags().String("store", "", "Data store backend ('mock' or 'mqtt', default: 'mock')")
	_ = cmd.PersistentFlags().MarkHidden("store")

	cmd.PersistentFlags().String("mqtt-url", "", "MQTT broker URL (e.g. tcp://broker.example:1883, tls://broker.example:8883)")
	cmd.PersistentFlags().String("mqtt-client-id", "", "MQTT client ID")
	cmd.PersistentFlags().String("mqtt-username", "", "MQTT username")
	cmd.PersistentFlags().String("mqtt-password", "", "MQTT password")
	cmd.PersistentFlags().Duration("mqtt-settle-timeout", 200*time.Millisecond, "Duration to wait after subscription for retained messages to settle")
	cmd.PersistentFlags().Duration("mqtt-heartbeat-timeout", 45*time.Second, "Max duration without heartbeat before marking machine offline")
	cmd.PersistentFlags().Duration("mqtt-heartbeat-check-interval", 10*time.Second, "Interval to check for stale machine heartbeats")

	// TLS flags
	cmd.PersistentFlags().Bool("mqtt-tls", false, "Enable TLS connection to MQTT broker")
	cmd.PersistentFlags().String("mqtt-ca-cert", "", "Path to CA certificate PEM file")
	cmd.PersistentFlags().String("mqtt-client-cert", "", "Path to client certificate PEM file")
	cmd.PersistentFlags().String("mqtt-client-key", "", "Path to client private key PEM file")
	cmd.PersistentFlags().Bool("mqtt-tls-insecure", false, "Skip broker TLS certificate verification")
	cmd.PersistentFlags().String("mqtt-server-name", "", "Override TLS SNI server name")
}

// loadActiveConfig loads configuration from --config flag or default path.
func loadActiveConfig(cmd *cobra.Command) (*config.Config, string, error) {
	var confPath string
	if cmd != nil {
		if val, err := cmd.Flags().GetString("config"); err == nil && val != "" {
			confPath = val
		}
	}
	if confPath == "" {
		confPath = config.DefaultConfigPath()
	}
	cfg, err := config.LoadConfig(confPath)
	return cfg, confPath, err
}

// resolveMQTTConfig aggregates MQTT options from config file, CLI flags, and environment variables.
func resolveMQTTConfig(cmd *cobra.Command) mqtt.Config {
	cfg := mqtt.DefaultConfig()

	// 1. Populate from config file if available
	if fileCfg, _, err := loadActiveConfig(cmd); err == nil && fileCfg != nil {
		if fileCfg.Broker.URL != "" {
			cfg.BrokerURL = fileCfg.Broker.URL
		}
		if fileCfg.Broker.Username != "" {
			cfg.Username = fileCfg.Broker.Username
		}
		if fileCfg.Broker.Password != "" {
			cfg.Password = fileCfg.Broker.Password
		}
		if fileCfg.Broker.TLS {
			cfg.TLSEnable = true
		}
		if fileCfg.Broker.CACertFile != "" {
			cfg.CACertFile = fileCfg.Broker.CACertFile
		}
		if fileCfg.Broker.ClientCertFile != "" {
			cfg.ClientCertFile = fileCfg.Broker.ClientCertFile
		}
		if fileCfg.Broker.ClientKeyFile != "" {
			cfg.ClientKeyFile = fileCfg.Broker.ClientKeyFile
		}
		if fileCfg.Broker.InsecureSkipVerify {
			cfg.InsecureSkipVerify = true
		}
		if fileCfg.Broker.ServerName != "" {
			cfg.ServerName = fileCfg.Broker.ServerName
		}
	}

	// 2. CLI flags override config file
	if cmd != nil {
		if val, err := cmd.Flags().GetString("mqtt-url"); err == nil && val != "" {
			cfg.BrokerURL = val
		}
		if val, err := cmd.Flags().GetString("mqtt-client-id"); err == nil && val != "" {
			cfg.ClientID = val
		}
		if val, err := cmd.Flags().GetString("mqtt-username"); err == nil && val != "" {
			cfg.Username = val
		}
		if val, err := cmd.Flags().GetString("mqtt-password"); err == nil && val != "" {
			cfg.Password = val
		}
		if val, err := cmd.Flags().GetDuration("mqtt-settle-timeout"); err == nil && cmd.Flags().Changed("mqtt-settle-timeout") {
			cfg.SettleTimeout = val
		}
		if val, err := cmd.Flags().GetDuration("mqtt-heartbeat-timeout"); err == nil && cmd.Flags().Changed("mqtt-heartbeat-timeout") {
			cfg.HeartbeatTimeout = val
		}
		if val, err := cmd.Flags().GetDuration("mqtt-heartbeat-check-interval"); err == nil && cmd.Flags().Changed("mqtt-heartbeat-check-interval") {
			cfg.HeartbeatCheckInterval = val
		}
		if val, err := cmd.Flags().GetBool("mqtt-tls"); err == nil && cmd.Flags().Changed("mqtt-tls") {
			cfg.TLSEnable = val
		}
		if val, err := cmd.Flags().GetString("mqtt-ca-cert"); err == nil && val != "" {
			cfg.CACertFile = val
		}
		if val, err := cmd.Flags().GetString("mqtt-client-cert"); err == nil && val != "" {
			cfg.ClientCertFile = val
		}
		if val, err := cmd.Flags().GetString("mqtt-client-key"); err == nil && val != "" {
			cfg.ClientKeyFile = val
		}
		if val, err := cmd.Flags().GetBool("mqtt-tls-insecure"); err == nil && cmd.Flags().Changed("mqtt-tls-insecure") {
			cfg.InsecureSkipVerify = val
		}
		if val, err := cmd.Flags().GetString("mqtt-server-name"); err == nil && val != "" {
			cfg.ServerName = val
		}
	}

	// 3. Environment variable fallbacks
	if cfg.BrokerURL == "" {
		if env := os.Getenv("AGENTPULSE_MQTT_URL"); env != "" {
			cfg.BrokerURL = env
		}
	}
	if env := os.Getenv("AGENTPULSE_MQTT_CLIENT_ID"); env != "" && (cmd == nil || !cmd.Flags().Changed("mqtt-client-id")) {
		cfg.ClientID = env
	}
	if cfg.Username == "" {
		if env := os.Getenv("AGENTPULSE_MQTT_USERNAME"); env != "" {
			cfg.Username = env
		}
	}
	if cfg.Password == "" {
		if env := os.Getenv("AGENTPULSE_MQTT_PASSWORD"); env != "" {
			cfg.Password = env
		}
	}
	if env := os.Getenv("AGENTPULSE_MQTT_SETTLE_TIMEOUT"); env != "" && (cmd == nil || !cmd.Flags().Changed("mqtt-settle-timeout")) {
		if d, err := time.ParseDuration(env); err == nil {
			cfg.SettleTimeout = d
		}
	}
	if env := os.Getenv("AGENTPULSE_MQTT_TIMEOUT"); env != "" {
		if d, err := time.ParseDuration(env); err == nil {
			cfg.Timeout = d
		}
	}
	if env := os.Getenv("AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT"); env != "" && (cmd == nil || !cmd.Flags().Changed("mqtt-heartbeat-timeout")) {
		if d, err := time.ParseDuration(env); err == nil {
			cfg.HeartbeatTimeout = d
		} else if sec, err := strconv.Atoi(env); err == nil && sec > 0 {
			cfg.HeartbeatTimeout = time.Duration(sec) * time.Second
		}
	}
	if env := os.Getenv("AGENTPULSE_MQTT_HEARTBEAT_CHECK_INTERVAL"); env != "" && (cmd == nil || !cmd.Flags().Changed("mqtt-heartbeat-check-interval")) {
		if d, err := time.ParseDuration(env); err == nil {
			cfg.HeartbeatCheckInterval = d
		} else if sec, err := strconv.Atoi(env); err == nil && sec > 0 {
			cfg.HeartbeatCheckInterval = time.Duration(sec) * time.Second
		}
	}

	// TLS environment variable fallbacks
	if !cfg.TLSEnable && (cmd == nil || !cmd.Flags().Changed("mqtt-tls")) {
		if env := os.Getenv("AGENTPULSE_MQTT_TLS"); env != "" {
			if b, err := strconv.ParseBool(strings.TrimSpace(env)); err == nil {
				cfg.TLSEnable = b
			}
		}
	}
	if cfg.CACertFile == "" {
		if env := os.Getenv("AGENTPULSE_MQTT_CA_CERT"); env != "" {
			cfg.CACertFile = env
		}
	}
	if cfg.ClientCertFile == "" {
		if env := os.Getenv("AGENTPULSE_MQTT_CLIENT_CERT"); env != "" {
			cfg.ClientCertFile = env
		}
	}
	if cfg.ClientKeyFile == "" {
		if env := os.Getenv("AGENTPULSE_MQTT_CLIENT_KEY"); env != "" {
			cfg.ClientKeyFile = env
		}
	}
	if !cfg.InsecureSkipVerify && (cmd == nil || !cmd.Flags().Changed("mqtt-tls-insecure")) {
		if env := os.Getenv("AGENTPULSE_MQTT_TLS_INSECURE"); env != "" {
			if b, err := strconv.ParseBool(strings.TrimSpace(env)); err == nil {
				cfg.InsecureSkipVerify = b
			}
		}
	}
	if cfg.ServerName == "" {
		if env := os.Getenv("AGENTPULSE_MQTT_SERVER_NAME"); env != "" {
			cfg.ServerName = env
		}
	}

	return cfg
}

// ResolveMQTTConfigForTest exposes resolveMQTTConfig for testing.
func ResolveMQTTConfigForTest(cmd *cobra.Command) mqtt.Config {
	return resolveMQTTConfig(cmd)
}

// determineStoreType decides whether to run mock or mqtt store.
func determineStoreType(cmd *cobra.Command) (string, error) {
	var st string
	if cmd != nil {
		if val, err := cmd.Flags().GetString("store"); err == nil && val != "" {
			st = strings.ToLower(strings.TrimSpace(val))
		}
	}
	if st == "" {
		st = strings.ToLower(strings.TrimSpace(os.Getenv("AGENTPULSE_STORE")))
	}

	if st != "" {
		if st != "mock" && st != "mqtt" {
			return "", fmt.Errorf("invalid store type %q: must be 'mock' or 'mqtt'", st)
		}
		return st, nil
	}

	// If no explicit store specified, check if broker URL is set in flags or config
	var hasMQTTURL bool
	if cmd != nil {
		if val, err := cmd.Flags().GetString("mqtt-url"); err == nil && val != "" {
			hasMQTTURL = true
		}
	}
	if !hasMQTTURL {
		if fileCfg, _, err := loadActiveConfig(cmd); err == nil && fileCfg != nil && fileCfg.Broker.URL != "" {
			hasMQTTURL = true
		}
	}
	if !hasMQTTURL && os.Getenv("AGENTPULSE_MQTT_URL") != "" {
		hasMQTTURL = true
	}

	if hasMQTTURL {
		return "mqtt", nil
	}

	return "mock", nil
}

// getStore creates and returns the configured store (mock or mqtt).
func getStore(cmd *cobra.Command) (store.Store, error) {
	stType, err := determineStoreType(cmd)
	if err != nil {
		return nil, err
	}

	if stType == "mqtt" {
		cfg := resolveMQTTConfig(cmd)
		if cfg.BrokerURL == "" {
			return nil, fmt.Errorf("MQTT store enabled but no broker URL provided (pass --mqtt-url or run 'agentpulse config')")
		}
		return mqtt.New(cfg)
	}

	return mock.NewMockStore(), nil
}

// testMQTTConnection tries to establish a short-lived connection to verify broker reachability.
func testMQTTConnection(cfg mqtt.Config, timeout time.Duration) error {
	doctorCfg := cfg
	doctorCfg.ClientID = cfg.ClientID + "-doctor"
	doctorCfg.Timeout = timeout

	opts := paho.NewClientOptions()
	if err := mqtt.ApplyClientOptions(opts, doctorCfg); err != nil {
		return fmt.Errorf("MQTT client options error: %w", err)
	}
	opts.SetAutoReconnect(false)

	client := paho.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(timeout) {
		return fmt.Errorf("connection timed out after %v", timeout)
	}
	if err := token.Error(); err != nil {
		return err
	}
	client.Disconnect(100)
	return nil
}


