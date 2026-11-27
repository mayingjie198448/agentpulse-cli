package mqtt_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

// generateTestCert creates a self-signed certificate and RSA private key in PEM format.
func generateTestCert(t *testing.T, isCA bool) (certPEM []byte, keyPEM []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("failed to generate serial number: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"AgentPulse Test CA"},
			CommonName:   "broker.example",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"broker.example", "localhost"},
	}

	if isCA {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes := x509.MarshalPKCS1PrivateKey(priv)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})
	return certPEM, keyPEM
}

func TestConfig_TLSInferenceAndEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		cfg          mqtt.Config
		wantTLS      bool
		wantEndpoint string
	}{
		{
			name: "plain tcp url without tls",
			cfg: mqtt.Config{
				BrokerURL: "tcp://broker.example:1883",
			},
			wantTLS:      false,
			wantEndpoint: "tcp://broker.example:1883",
		},
		{
			name: "plain host port without scheme and without tls",
			cfg: mqtt.Config{
				BrokerURL: "broker.example:1883",
			},
			wantTLS:      false,
			wantEndpoint: "tcp://broker.example:1883",
		},
		{
			name: "tls:// scheme infers tls",
			cfg: mqtt.Config{
				BrokerURL: "tls://broker.example:8883",
			},
			wantTLS:      true,
			wantEndpoint: "tls://broker.example:8883",
		},
		{
			name: "ssl:// scheme infers tls",
			cfg: mqtt.Config{
				BrokerURL: "ssl://broker.example:8883",
			},
			wantTLS:      true,
			wantEndpoint: "ssl://broker.example:8883",
		},
		{
			name: "mqtts:// scheme infers tls",
			cfg: mqtt.Config{
				BrokerURL: "mqtts://broker.example:8883",
			},
			wantTLS:      true,
			wantEndpoint: "mqtts://broker.example:8883",
		},
		{
			name: "tcps:// scheme infers tls",
			cfg: mqtt.Config{
				BrokerURL: "tcps://broker.example:8883",
			},
			wantTLS:      true,
			wantEndpoint: "tcps://broker.example:8883",
		},
		{
			name: "explicit TLSEnable converts tcp:// to tls://",
			cfg: mqtt.Config{
				BrokerURL: "tcp://broker.example:8883",
				TLSEnable: true,
			},
			wantTLS:      true,
			wantEndpoint: "tls://broker.example:8883",
		},
		{
			name: "explicit TLSEnable prepends tls:// to schemeless url",
			cfg: mqtt.Config{
				BrokerURL: "broker.example:8883",
				TLSEnable: true,
			},
			wantTLS:      true,
			wantEndpoint: "tls://broker.example:8883",
		},
		{
			name: "CACertFile infers tls",
			cfg: mqtt.Config{
				BrokerURL:  "tcp://broker.example:8883",
				CACertFile: "/path/ca.pem",
			},
			wantTLS:      true,
			wantEndpoint: "tls://broker.example:8883",
		},
		{
			name: "ClientCertFile infers tls",
			cfg: mqtt.Config{
				BrokerURL:      "broker.example:8883",
				ClientCertFile: "/path/client.pem",
			},
			wantTLS:      true,
			wantEndpoint: "tls://broker.example:8883",
		},
		{
			name: "InsecureSkipVerify infers tls",
			cfg: mqtt.Config{
				BrokerURL:          "tcp://broker.example:8883",
				InsecureSkipVerify: true,
			},
			wantTLS:      true,
			wantEndpoint: "tls://broker.example:8883",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsTLSEnabled(); got != tt.wantTLS {
				t.Errorf("IsTLSEnabled() = %v, want %v", got, tt.wantTLS)
			}
			if got := tt.cfg.BrokerEndpoint(); got != tt.wantEndpoint {
				t.Errorf("BrokerEndpoint() = %q, want %q", got, tt.wantEndpoint)
			}
		})
	}
}

func TestConfig_TLSConfig(t *testing.T) {
	t.Run("tls disabled returns nil", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL: "tcp://broker.example:1883",
		}
		tlsCfg, err := cfg.TLSConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tlsCfg != nil {
			t.Fatalf("expected nil tls.Config, got %+v", tlsCfg)
		}
	})

	t.Run("tls:// URL enables TLS and sets ServerName", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL: "tls://broker.example:8883",
		}
		tlsCfg, err := cfg.TLSConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tlsCfg == nil {
			t.Fatalf("expected non-nil tls.Config")
		}
		if tlsCfg.InsecureSkipVerify {
			t.Errorf("expected InsecureSkipVerify=false")
		}
		if tlsCfg.ServerName != "broker.example" {
			t.Errorf("expected ServerName='broker.example', got %q", tlsCfg.ServerName)
		}
	})

	t.Run("custom ServerName override", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:  "tls://127.0.0.1:8883",
			ServerName: "custom.server.name",
		}
		tlsCfg, err := cfg.TLSConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tlsCfg.ServerName != "custom.server.name" {
			t.Errorf("expected ServerName='custom.server.name', got %q", tlsCfg.ServerName)
		}
	})

	t.Run("insecure skip verify flag", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:          "tls://broker.example:8883",
			InsecureSkipVerify: true,
		}
		tlsCfg, err := cfg.TLSConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !tlsCfg.InsecureSkipVerify {
			t.Errorf("expected InsecureSkipVerify=true")
		}
	})

	t.Run("valid CA cert file", func(t *testing.T) {
		caCertPEM, _ := generateTestCert(t, true)
		tmpDir := t.TempDir()
		caFile := filepath.Join(tmpDir, "ca.pem")
		if err := os.WriteFile(caFile, caCertPEM, 0600); err != nil {
			t.Fatalf("failed to write CA file: %v", err)
		}

		cfg := mqtt.Config{
			BrokerURL:  "tls://broker.example:8883",
			CACertFile: caFile,
		}
		tlsCfg, err := cfg.TLSConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tlsCfg.RootCAs == nil {
			t.Fatalf("expected RootCAs to be populated")
		}
	})

	t.Run("missing CA cert file returns clear error", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:  "tls://broker.example:8883",
			CACertFile: "/nonexistent/ca.pem",
		}
		_, err := cfg.TLSConfig()
		if err == nil {
			t.Fatalf("expected error for missing CA cert file, got nil")
		}
		if !strings.Contains(err.Error(), "failed to read CA certificate") {
			t.Errorf("expected error message to mention 'failed to read CA certificate', got: %v", err)
		}
	})

	t.Run("corrupt CA cert file returns clear error", func(t *testing.T) {
		tmpDir := t.TempDir()
		caFile := filepath.Join(tmpDir, "corrupt-ca.pem")
		if err := os.WriteFile(caFile, []byte("NOT A REAL PEM"), 0600); err != nil {
			t.Fatalf("failed to write corrupt CA file: %v", err)
		}

		cfg := mqtt.Config{
			BrokerURL:  "tls://broker.example:8883",
			CACertFile: caFile,
		}
		_, err := cfg.TLSConfig()
		if err == nil {
			t.Fatalf("expected error for corrupt CA cert file, got nil")
		}
		if !strings.Contains(err.Error(), "no PEM certificates found") {
			t.Errorf("expected error message to mention 'no PEM certificates found', got: %v", err)
		}
	})

	t.Run("valid mTLS client cert and key", func(t *testing.T) {
		clientCertPEM, clientKeyPEM := generateTestCert(t, false)
		tmpDir := t.TempDir()
		certFile := filepath.Join(tmpDir, "client.crt")
		keyFile := filepath.Join(tmpDir, "client.key")
		if err := os.WriteFile(certFile, clientCertPEM, 0600); err != nil {
			t.Fatalf("failed to write client cert file: %v", err)
		}
		if err := os.WriteFile(keyFile, clientKeyPEM, 0600); err != nil {
			t.Fatalf("failed to write client key file: %v", err)
		}

		cfg := mqtt.Config{
			BrokerURL:      "tls://broker.example:8883",
			ClientCertFile: certFile,
			ClientKeyFile:  keyFile,
		}
		tlsCfg, err := cfg.TLSConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tlsCfg.Certificates) != 1 {
			t.Fatalf("expected 1 client certificate, got %d", len(tlsCfg.Certificates))
		}
	})

	t.Run("mTLS missing key returns clear error", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:      "tls://broker.example:8883",
			ClientCertFile: "/path/client.crt",
		}
		_, err := cfg.TLSConfig()
		if err == nil {
			t.Fatalf("expected error for missing client key, got nil")
		}
		if !strings.Contains(err.Error(), "both --mqtt-client-cert and --mqtt-client-key must be provided") {
			t.Errorf("expected error message about both cert and key, got: %v", err)
		}
	})

	t.Run("mTLS missing cert returns clear error", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:     "tls://broker.example:8883",
			ClientKeyFile: "/path/client.key",
		}
		_, err := cfg.TLSConfig()
		if err == nil {
			t.Fatalf("expected error for missing client cert, got nil")
		}
		if !strings.Contains(err.Error(), "both --mqtt-client-cert and --mqtt-client-key must be provided") {
			t.Errorf("expected error message about both cert and key, got: %v", err)
		}
	})

	t.Run("mTLS non-existent files return clear error", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:      "tls://broker.example:8883",
			ClientCertFile: "/nonexistent/client.crt",
			ClientKeyFile:  "/nonexistent/client.key",
		}
		_, err := cfg.TLSConfig()
		if err == nil {
			t.Fatalf("expected error for missing client cert files, got nil")
		}
		if !strings.Contains(err.Error(), "failed to load client certificate/key pair") {
			t.Errorf("expected error message to mention 'failed to load client certificate/key pair', got: %v", err)
		}
	})
}

func TestConfig_ApplyClientOptions(t *testing.T) {
	t.Run("wires all fields including TLS", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:          "tls://broker.example:8883",
			ClientID:           "client-test-01",
			Username:           "test_user",
			Password:           "test_pass",
			KeepAlive:          15 * time.Second,
			Timeout:            3 * time.Second,
			InsecureSkipVerify: true,
		}

		opts := paho.NewClientOptions()
		if err := mqtt.ApplyClientOptions(opts, cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		reader := paho.NewOptionsReader(opts)
		servers := reader.Servers()
		if len(servers) != 1 || servers[0].String() != "tls://broker.example:8883" {
			t.Errorf("expected broker 'tls://broker.example:8883', got %v", servers)
		}
		if reader.ClientID() != "client-test-01" {
			t.Errorf("expected clientID 'client-test-01', got %q", reader.ClientID())
		}
		if reader.Username() != "test_user" {
			t.Errorf("expected username 'test_user', got %q", reader.Username())
		}
		if reader.Password() != "test_pass" {
			t.Errorf("expected password 'test_pass', got %q", reader.Password())
		}
		if reader.KeepAlive() != 15*time.Second {
			t.Errorf("expected keepalive 15s, got %v", reader.KeepAlive())
		}
		if reader.ConnectTimeout() != 3*time.Second {
			t.Errorf("expected timeout 3s, got %v", reader.ConnectTimeout())
		}
		if reader.TLSConfig() == nil {
			t.Fatalf("expected TLSConfig to be set")
		}
		if !reader.TLSConfig().InsecureSkipVerify {
			t.Errorf("expected InsecureSkipVerify=true")
		}
	})

	t.Run("fails when TLS config error occurs", func(t *testing.T) {
		cfg := mqtt.Config{
			BrokerURL:  "tls://broker.example:8883",
			CACertFile: "/nonexistent/ca.pem",
		}
		opts := paho.NewClientOptions()
		err := mqtt.ApplyClientOptions(opts, cfg)
		if err == nil {
			t.Fatalf("expected ApplyClientOptions to fail on missing CA cert")
		}
		if !strings.Contains(err.Error(), "failed to read CA certificate") {
			t.Errorf("expected CA read error, got: %v", err)
		}
	})
}

func TestConfig_Validate_TLS(t *testing.T) {
	t.Run("valid TLS config passes Validate", func(t *testing.T) {
		cfg := mqtt.DefaultConfig()
		cfg.BrokerURL = "tls://broker.example:8883"
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected validation to pass, got: %v", err)
		}
	})

	t.Run("invalid CA cert fails Validate", func(t *testing.T) {
		cfg := mqtt.DefaultConfig()
		cfg.BrokerURL = "tls://broker.example:8883"
		cfg.CACertFile = "/nonexistent/ca.pem"
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected validation to fail on missing CA cert")
		}
	})
}
