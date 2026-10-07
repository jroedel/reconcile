package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Addr != defaultAddr {
		t.Errorf("addr = %q, want %q", cfg.Server.Addr, defaultAddr)
	}

	if cfg.Server.ShutdownGrace.Duration != 15*time.Second {
		t.Errorf("shutdown_grace = %s, want 15s", cfg.Server.ShutdownGrace.Duration)
	}
}

// The example is the documentation, so it has to be a file the binary accepts.
func TestLoadConfigExample(t *testing.T) {
	if _, err := loadConfig("../../config.example.toml"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigRefusesUnknownKeys(t *testing.T) {
	_, err := loadConfig(writeConfig(t, "[server]\nadress = \"127.0.0.1:1\"\n"))
	if err == nil || !strings.Contains(err.Error(), "server.adress") {
		t.Fatalf("err = %v, want one naming server.adress", err)
	}
}

func TestLoadConfigRefusesBadDuration(t *testing.T) {
	_, err := loadConfig(writeConfig(t, "[server]\nshutdown_grace = \"soon\"\n"))
	if err == nil || !strings.Contains(err.Error(), "15s") {
		t.Fatalf("err = %v, want one saying how to write a duration", err)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := loadConfig(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil || !strings.Contains(err.Error(), "config.example.toml") {
		t.Fatalf("err = %v, want one pointing at the example", err)
	}
}

// deploy/deploy.sh greps -check's output for this exact shape to confirm the
// new binary listens where Apache proxies.
func TestSummaryNamesTheAddressForTheDeploy(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}

	first, _, _ := strings.Cut(cfg.summary(), "\n")
	if first != "listening on   "+defaultAddr {
		t.Fatalf("first line = %q", first)
	}
}
