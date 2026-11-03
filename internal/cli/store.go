package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/mock"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/spf13/cobra"
)

func registerStoreFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("store", "", "Data store backend ('mock' or 'mqtt', default: 'mock')")
	cmd.PersistentFlags().String("mqtt-url", "", "MQTT broker URL (e.g. tcp://broker.example:1883 or $AGENTPULSE_MQTT_URL)")
	cmd.PersistentFlags().String("mqtt-client-id", "", "MQTT client ID (or $AGENTPULSE_MQTT_CLIENT_ID)")
	cmd.PersistentFlags().String("mqtt-username", "", "MQTT username (or $AGENTPULSE_MQTT_USERNAME)")
	cmd.PersistentFlags().String("mqtt-password", "", "MQTT password (or $AGENTPULSE_MQTT_PASSWORD)")
	cmd.PersistentFlags().Duration("mqtt-settle-timeout", 200*time.Millisecond, "Duration to wait after subscription for retained messages to settle (or $AGENTPULSE_MQTT_SETTLE_TIMEOUT)")
}

// resolveMQTTConfig aggregates MQTT options from CLI flags and environment variables.
func resolveMQTTConfig(cmd *cobra.Command) mqtt.Config {
	cfg := mqtt.DefaultConfig()

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
	}

	// Environment variable fallbacks
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

	return cfg
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

	// If no explicit store specified, check if broker URL is set
	var hasMQTTURL bool
	if cmd != nil {
		if val, err := cmd.Flags().GetString("mqtt-url"); err == nil && val != "" {
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
			return nil, fmt.Errorf("MQTT store enabled but no broker URL provided (pass --mqtt-url or set AGENTPULSE_MQTT_URL)")
		}
		return mqtt.New(cfg)
	}

	return mock.NewMockStore(), nil
}

// testMQTTConnection tries to establish a short-lived connection to verify broker reachability.
func testMQTTConnection(cfg mqtt.Config, timeout time.Duration) error {
	opts := paho.NewClientOptions()
	opts.AddBroker(cfg.BrokerURL)
	opts.SetClientID(cfg.ClientID + "-doctor")
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	opts.SetConnectTimeout(timeout)
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
