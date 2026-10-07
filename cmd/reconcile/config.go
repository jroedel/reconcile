package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// config is the whole runtime configuration, read from a TOML file.
//
// A file rather than environment variables, because the deploy target is a
// shared hosting account with no systemd and therefore no EnvironmentFile: the
// process is started by a shell script from cron, and a file it reads is the
// only place a secret can live that is not the command line. Secrets reach the
// server from a person's machine over ssh (make deploy-send-secrets), never
// through GitHub.
//
// Every field is tagged explicitly. The decoder matches a key to a field name
// case-insensitively, so `shutdown_grace` does not find `ShutdownGrace` -- and
// because an unrecognised key is a startup error here, an untagged field turns
// a correct config file into a refusal to start.
type config struct {
	Server struct {
		Addr          string   `toml:"addr"`
		ShutdownGrace duration `toml:"shutdown_grace"`
	} `toml:"server"`
	DB struct {
		Path string `toml:"path"`
	} `toml:"db"`
	Log struct {
		Level string `toml:"level"`
		File  string `toml:"file"`
	} `toml:"log"`
}

// defaultAddr is a loopback port no sibling project on the same account uses:
// dropin-forms has 8410 and 8411, mass-intentions 8431, stewards 8451.
// deploy/htaccess.template and secrets.env.example say the same; change all
// three or none.
const defaultAddr = "127.0.0.1:8461"

// defaultDB is the database's name when the config does not give one. The
// deploy's backup looks for exactly this name in APP_DIR.
const defaultDB = "reconcile.db"

// duration is a time.Duration that TOML can read as "15s".
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("%q is not a duration; write it like 15s or 2m", text)
	}

	d.Duration = parsed

	return nil
}

// loadConfig reads and validates the file, or explains what is wrong with it.
//
// An unrecognised key is an error rather than a warning. A mistyped key in a
// file that is silently accepted is a setting that looks configured and is not,
// and the way that surfaces is a production incident rather than a startup
// message.
func loadConfig(path string) (config, error) {
	var cfg config

	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("there is no config file at %s; copy config.example.toml to %s and edit it", path, path)
		}

		return cfg, fmt.Errorf("reading %s: %w", path, err)
	}

	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}

		return cfg, fmt.Errorf("%s has settings this binary does not know: %s. Check them for a typo", path, strings.Join(names, ", "))
	}

	if cfg.Server.Addr == "" {
		cfg.Server.Addr = defaultAddr
	}

	if cfg.Server.ShutdownGrace.Duration == 0 {
		cfg.Server.ShutdownGrace.Duration = 15 * time.Second
	}

	// A default rather than a required key. On the server run.sh starts the
	// binary from APP_DIR, so the default lands beside it, outside
	// public_html, which is where deploy.sh backs it up from. Required, it
	// would also have to arrive in config.toml before the binary that reads
	// it -- and the binary already running refuses a key it does not know.
	if cfg.DB.Path == "" {
		cfg.DB.Path = defaultDB
	}

	return cfg, nil
}

// summary is what -check prints. It names every setting that decides
// behaviour, and will never name one that is a secret.
//
// deploy/deploy.sh reads the first line, so its wording is a contract: the
// pre-flight greps for "listening on" and the address Apache proxies to.
func (c config) summary() string {
	var b strings.Builder

	fmt.Fprintf(&b, "listening on   %s\n", c.Server.Addr)
	fmt.Fprintf(&b, "database       %s\n", c.DB.Path)
	fmt.Fprintf(&b, "log level      %s\n", orElse(c.Log.Level, "info"))
	fmt.Fprintf(&b, "log file       %s\n", orElse(c.Log.File, "(stderr)"))
	fmt.Fprintf(&b, "shutdown grace %s\n", c.Server.ShutdownGrace.Duration)

	return b.String()
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}

	return s
}

// openLog returns the destination for log lines, and whether the caller owns
// it. Empty means stderr, which is what the supervisor on the server redirects
// into a file -- so the process never has to know it is being supervised.
func openLog(path string) (*os.File, bool, error) {
	if path == "" {
		return os.Stderr, false, nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("opening the log file %s: %w", path, err)
	}

	return f, true, nil
}
