package mqtt

import (
	"fmt"
	"math/rand"
	"os"
	"time"
)

// Config configures the MQTT Store client connection and runtime buffers.
type Config struct {
	BrokerURL              string        // e.g. "tcp://broker.example:1883"
	ClientID               string        // Client identifier; auto-generated if empty
	Username               string        // Optional MQTT broker username
	Password               string        // Optional MQTT broker password
	KeepAlive              time.Duration // MQTT keepalive interval (default: 30s)
	Timeout                time.Duration // Connect and request timeout (default: 5s)
	LogCap                 int           // Maximum logs retained per job in ring buffer (default: 500)
	BufferCap              int           // Buffer capacity for event subscribers (default: 1024)
	HeartbeatTimeout       time.Duration // Max duration before marking node offline (default: 45s)
	HeartbeatCheckInterval time.Duration // Interval to run offline checker (default: 10s)
	SettleTimeout          time.Duration // Wait duration after initial subscription for retained messages to settle (default: 200ms)
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		BrokerURL:              "",
		ClientID:               generateClientID(),
		KeepAlive:              30 * time.Second,
		Timeout:                5 * time.Second,
		LogCap:                 500,
		BufferCap:              1024,
		HeartbeatTimeout:       45 * time.Second,
		HeartbeatCheckInterval: 10 * time.Second,
		SettleTimeout:          200 * time.Millisecond,
	}
}

// Validate checks and applies default values to zero-valued configuration fields.
func (c *Config) Validate() error {
	if c.BrokerURL == "" {
		return fmt.Errorf("MQTT broker URL cannot be empty")
	}
	if c.ClientID == "" {
		c.ClientID = generateClientID()
	}
	if c.KeepAlive <= 0 {
		c.KeepAlive = 30 * time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.LogCap <= 0 {
		c.LogCap = 500
	}
	if c.BufferCap <= 0 {
		c.BufferCap = 1024
	}
	if c.HeartbeatTimeout <= 0 {
		c.HeartbeatTimeout = 45 * time.Second
	}
	if c.HeartbeatCheckInterval <= 0 {
		c.HeartbeatCheckInterval = 10 * time.Second
	}
	if c.SettleTimeout < 0 {
		c.SettleTimeout = 0
	} else if c.SettleTimeout == 0 {
		c.SettleTimeout = 200 * time.Millisecond
	}
	return nil
}

func generateClientID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("agentpulse-cli-%d-%04d", os.Getpid(), r.Intn(10000))
}
