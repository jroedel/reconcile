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

	// deploy.sh backs up exactly this name in APP_DIR.
	if cfg.DB.Path != "reconcile.db" {
		t.Errorf("db path = %q, want reconcile.db", cfg.DB.Path)
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

func TestLoadConfigBaseURL(t *testing.T) {
	for give, want := range map[string]string{
		"https://reconcile.example.org":  "https://reconcile.example.org",
		"https://reconcile.example.org/": "https://reconcile.example.org",
		"http://127.0.0.1:8461":          "http://127.0.0.1:8461",
		"http://localhost:8461":          "http://localhost:8461",
	} {
		cfg, err := loadConfig(writeConfig(t, "[server]\nbase_url = \""+give+"\"\n"))
		if err != nil {
			t.Errorf("%s: %v", give, err)
			continue
		}

		if cfg.Server.BaseURL != want {
			t.Errorf("%s: base_url = %q, want %q", give, cfg.Server.BaseURL, want)
		}
	}

	for _, give := range []string{
		"http://reconcile.example.org",       // the Secure cookie would never come back
		"https://reconcile.example.org/app",  // a path
		"https://reconcile.example.org/?x=1", // a query
		"reconcile.example.org",              // no scheme
	} {
		if _, err := loadConfig(writeConfig(t, "[server]\nbase_url = \""+give+"\"\n")); err == nil {
			t.Errorf("%s was accepted", give)
		}
	}
}

func TestLoadConfigMail(t *testing.T) {
	// The host's own Exim: no account, defaults for the rest.
	cfg, err := loadConfig(writeConfig(t, "[mail]\nhost = \"localhost\"\nport = 25\nfrom = \"codes@example.org\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Mail.FromName == "" {
		t.Error("from_name has no default")
	}

	if !strings.Contains(cfg.summary(), "localhost:25, from codes@example.org") {
		t.Errorf("-check does not name the relay:\n%s", cfg.summary())
	}

	for name, body := range map[string]string{
		"no from":                 "[mail]\nhost = \"smtp.example.org\"\n",
		"a user with no password": "[mail]\nhost = \"smtp.example.org\"\nfrom = \"a@example.org\"\nuser = \"a\"\n",
		"a password with no user": "[mail]\nhost = \"smtp.example.org\"\nfrom = \"a@example.org\"\npassword = \"p\"\n",
	} {
		if _, err := loadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestLoadConfigBootstrapSecret(t *testing.T) {
	if _, err := loadConfig(writeConfig(t, "[auth]\nbootstrap_secret = \"short\"\n")); err == nil {
		t.Error("a short bootstrap secret was accepted")
	}

	cfg, err := loadConfig(writeConfig(t, "[auth]\nbootstrap_secret = \""+strings.Repeat("x", 32)+"\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	// -check names whether there is one, never what it is.
	if strings.Contains(cfg.summary(), "xxxx") {
		t.Errorf("-check prints the secret:\n%s", cfg.summary())
	}
}

// Nothing secret may reach -check's output: the deploy prints it.
func TestSummaryNeverPrintsThePassword(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, "[mail]\nhost = \"smtp.example.org\"\nfrom = \"a@example.org\"\nuser = \"a\"\npassword = \"hunter2-but-longer\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(cfg.summary(), "hunter2") {
		t.Errorf("-check prints the relay password:\n%s", cfg.summary())
	}
}
