package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Config configures the MQTT Store client connection and runtime buffers.
type Config struct {
	BrokerURL              string        // e.g. "tcp://broker.example:1883" or "tls://broker.example:8883"
	ClientID               string        // Client identifier; auto-generated if empty
	Username               string        // Optional MQTT broker username
	Password               string        // Optional MQTT broker password
	KeepAlive              time.Duration // MQTT keepalive interval (default: 30s)
	Timeout                time.Duration // Connect and request timeout (default: 10s)
	LogCap                 int           // Maximum logs retained per job in ring buffer (default: 500)
	BufferCap              int           // Buffer capacity for event subscribers (default: 1024)
	HeartbeatTimeout       time.Duration // Max duration before marking node offline (default: 45s)
	HeartbeatCheckInterval time.Duration // Interval to run offline checker (default: 10s)
	SettleTimeout          time.Duration // Wait duration after initial subscription for retained messages to settle (default: 200ms)

	// TLS Configuration fields
	TLSEnable          bool   // Force enable TLS (also auto-enabled by tls:// scheme or cert flags)
	CACertFile         string // Optional path to CA certificate PEM file
	ClientCertFile     string // Optional path to client certificate PEM file (for mTLS)
	ClientKeyFile      string // Optional path to client private key PEM file (for mTLS)
	InsecureSkipVerify bool   // Skip server certificate verification (DANGER: lab/test only)
	ServerName         string // Optional SNI server name override
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		BrokerURL:              "",
		ClientID:               generateClientID(),
		KeepAlive:              30 * time.Second,
		Timeout:                10 * time.Second,
		LogCap:                 500,
		BufferCap:              1024,
		HeartbeatTimeout:       45 * time.Second,
		HeartbeatCheckInterval: 10 * time.Second,
		SettleTimeout:          200 * time.Millisecond,
	}
}

// isTLSScheme returns true if rawURL specifies a TLS-encrypted MQTT scheme.
func isTLSScheme(rawURL string) bool {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return false
	}
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "tls://") ||
		strings.HasPrefix(lower, "ssl://") ||
		strings.HasPrefix(lower, "mqtts://") ||
		strings.HasPrefix(lower, "tcps://") ||
		strings.HasPrefix(lower, "mqtt+ssl://") ||
		strings.HasPrefix(lower, "wss://")
}

// extractHostname parses host without port from a broker URL.
func extractHostname(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "tcp://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// IsTLSEnabled reports whether TLS should be active based on explicit enable,
// URL scheme (e.g. tls://, ssl://, mqtts://), or TLS certificate configuration.
func (c *Config) IsTLSEnabled() bool {
	if c.TLSEnable {
		return true
	}
	if c.CACertFile != "" || c.ClientCertFile != "" || c.ClientKeyFile != "" || c.InsecureSkipVerify {
		return true
	}
	return isTLSScheme(c.BrokerURL)
}

// BrokerEndpoint returns the normalized broker URL for Paho MQTT.
// When TLS is enabled and the scheme is "tcp://" or omitted, it converts the scheme to "tls://".
func (c *Config) BrokerEndpoint() string {
	raw := strings.TrimSpace(c.BrokerURL)
	if raw == "" {
		return ""
	}
	if c.IsTLSEnabled() {
		if strings.HasPrefix(strings.ToLower(raw), "tcp://") {
			return "tls://" + raw[6:]
		}
		if !strings.Contains(raw, "://") {
			return "tls://" + raw
		}
		return raw
	}
	if !strings.Contains(raw, "://") {
		return "tcp://" + raw
	}
	return raw
}

// TLSConfig builds a *tls.Config from configuration options.
// Returns nil if TLS is not enabled.
func (c *Config) TLSConfig() (*tls.Config, error) {
	if !c.IsTLSEnabled() {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: c.InsecureSkipVerify,
	}

	if c.CACertFile != "" {
		caData, err := os.ReadFile(c.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate %q: %w", c.CACertFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, fmt.Errorf("failed to parse CA certificate from %q: no PEM certificates found", c.CACertFile)
		}
		tlsConfig.RootCAs = pool
	}

	if c.ClientCertFile != "" || c.ClientKeyFile != "" {
		if c.ClientCertFile == "" || c.ClientKeyFile == "" {
			return nil, fmt.Errorf("both --mqtt-client-cert and --mqtt-client-key must be provided for mTLS")
		}
		cert, err := tls.LoadX509KeyPair(c.ClientCertFile, c.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate/key pair (%q, %q): %w", c.ClientCertFile, c.ClientKeyFile, err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if c.ServerName != "" {
		tlsConfig.ServerName = c.ServerName
	} else if c.BrokerURL != "" {
		if host := extractHostname(c.BrokerURL); host != "" {
			tlsConfig.ServerName = host
		}
	}

	return tlsConfig, nil
}

// ApplyClientOptions sets connection parameters, credentials, and TLS options on Paho ClientOptions.
func ApplyClientOptions(opts *paho.ClientOptions, cfg Config) error {
	endpoint := cfg.BrokerEndpoint()
	if endpoint != "" {
		opts.AddBroker(endpoint)
	}
	if cfg.ClientID != "" {
		opts.SetClientID(cfg.ClientID)
	}
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	if cfg.KeepAlive > 0 {
		opts.SetKeepAlive(cfg.KeepAlive)
	}
	if cfg.Timeout > 0 {
		opts.SetConnectTimeout(cfg.Timeout)
	}

	if cfg.IsTLSEnabled() {
		tlsConfig, err := cfg.TLSConfig()
		if err != nil {
			return err
		}
		opts.SetTLSConfig(tlsConfig)
	}

	return nil
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
		c.Timeout = 10 * time.Second
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

	if c.IsTLSEnabled() {
		if _, err := c.TLSConfig(); err != nil {
			return err
		}
	}

	return nil
}

func generateClientID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("agentpulse-cli-%d-%04d", os.Getpid(), r.Intn(10000))
}
