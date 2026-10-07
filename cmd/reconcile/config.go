package main

import (
	"fmt"
	"net"
	"net/url"
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
// only place a secret can live that is not the command line. The secrets are
// the mail relay's password, if it has one, and the one-time bootstrap secret,
// and they reach the server from a person's machine over ssh (make
// deploy-send-secrets), never through GitHub.
//
// Every field is tagged explicitly. The decoder matches a key to a field name
// case-insensitively, so `shutdown_grace` does not find `ShutdownGrace` -- and
// because an unrecognised key is a startup error here, an untagged field turns
// a correct config file into a refusal to start.
type config struct {
	Server struct {
		Addr string `toml:"addr"`

		// BaseURL is the public https address: the origin every write must
		// come from. From here and never from the request, because behind
		// the host's proxy the Host header is the loopback. Empty means
		// sign-in is off.
		BaseURL string `toml:"base_url"`

		// TrustProxy believes X-Forwarded-For: true behind the host's
		// Apache, which writes it, and false anywhere nothing does, where
		// it is whatever a client sent (web.ClientIP).
		TrustProxy    bool     `toml:"trust_proxy"`
		ShutdownGrace duration `toml:"shutdown_grace"`
	} `toml:"server"`
	DB struct {
		Path string `toml:"path"`
	} `toml:"db"`

	// Files is where uploaded statements and receipts are kept.
	Files struct {
		Dir string `toml:"dir"`
	} `toml:"files"`
	Log struct {
		Level string `toml:"level"`
		File  string `toml:"file"`
	} `toml:"log"`

	Auth struct {
		// BootstrapSecret makes the first person to present it the site
		// administrator, once, ever. Empty means its page is not served.
		BootstrapSecret string `toml:"bootstrap_secret"`
	} `toml:"auth"`

	// Mail is the relay sign-in codes go out through. Without it the app
	// runs, and nobody can be sent a code: -check says so in capitals.
	Mail struct {
		Host     string `toml:"host"`
		Port     int    `toml:"port"`
		User     string `toml:"user"`
		Password string `toml:"password"`
		From     string `toml:"from"`
		FromName string `toml:"from_name"`
	} `toml:"mail"`
}

// minBootstrap is the shortest bootstrap secret the binary accepts. It makes
// somebody the site administrator with no email, so it is a password for as
// long as it is unspent; make bootstrap-secret prints one of 48.
const minBootstrap = 32

// defaultAddr is a loopback port no sibling project on the same account uses:
// dropin-forms has 8410 and 8411, mass-intentions 8431, stewards 8451.
// deploy/htaccess.template and secrets.env.example say the same; change all
// three or none.
const defaultAddr = "127.0.0.1:8461"

// defaultDB is the database's name when the config does not give one. The
// deploy's backup looks for exactly this name in APP_DIR.
const defaultDB = "reconcile.db"

// defaultFiles is the uploads' directory when the config does not give one.
// deploy.sh snapshots exactly this name in APP_DIR beside each backup.
const defaultFiles = "files"

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

	// The same reasoning, for the same reason: deploy.sh looks for it there.
	if cfg.Files.Dir == "" {
		cfg.Files.Dir = defaultFiles
	}

	if cfg.Server.BaseURL != "" {
		base, err := url.Parse(cfg.Server.BaseURL)

		switch {
		case err != nil || base.Host == "" || (base.Path != "" && base.Path != "/") || base.RawQuery != "":
			return cfg, fmt.Errorf("%s has [server] base_url %q; write only the scheme and host, such as https://reconcile.example.org", path, cfg.Server.BaseURL)
		case base.Scheme != "https" && !(base.Scheme == "http" && isLoopback(base.Hostname())):
			// http only for a developer's own machine. Anywhere else the
			// session cookie, which is Secure, would never come back.
			return cfg, fmt.Errorf("%s has [server] base_url %q; it must start https:// unless it is this machine", path, cfg.Server.BaseURL)
		}

		cfg.Server.BaseURL = base.Scheme + "://" + base.Host
	}

	if cfg.Mail.Host != "" {
		if cfg.Mail.From == "" {
			return cfg, fmt.Errorf("%s sets [mail] host but not from; sign-in codes would fail at the first attempt", path)
		}

		// Both or neither. The host's own Exim on localhost needs no
		// account; a relay elsewhere needs both halves of one, and a user
		// with no password is a relay somebody stopped filling in halfway.
		if (cfg.Mail.User == "") != (cfg.Mail.Password == "") {
			return cfg, fmt.Errorf("%s sets one of [mail] user and password but not the other", path)
		}

		if cfg.Mail.Port == 0 {
			cfg.Mail.Port = 587
		}

		if cfg.Mail.FromName == "" {
			cfg.Mail.FromName = "Reconcile"
		}
	}

	if n := len(cfg.Auth.BootstrapSecret); n > 0 && n < minBootstrap {
		return cfg, fmt.Errorf("%s has an [auth] bootstrap_secret of %d characters; it makes somebody the administrator with no email, so it needs at least %d. Run make bootstrap-secret for one", path, n, minBootstrap)
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
	fmt.Fprintf(&b, "public address %s\n", orElse(c.Server.BaseURL, "NOT SET - sign-in is off"))
	client := "from the connection"
	if c.Server.TrustProxy {
		client = "from X-Forwarded-For (behind a proxy)"
	}

	fmt.Fprintf(&b, "client address %s\n", client)
	fmt.Fprintf(&b, "database       %s\n", c.DB.Path)
	fmt.Fprintf(&b, "uploads        %s\n", c.Files.Dir)
	fmt.Fprintf(&b, "log level      %s\n", orElse(c.Log.Level, "info"))
	fmt.Fprintf(&b, "log file       %s\n", orElse(c.Log.File, "(stderr)"))
	fmt.Fprintf(&b, "shutdown grace %s\n", c.Server.ShutdownGrace.Duration)

	mail := "NOT CONFIGURED - nobody can be sent a sign-in code"
	if strings.HasPrefix(c.Server.BaseURL, "http://") {
		// Only ever loopback: loadConfig refuses http anywhere else.
		mail = "none - codes are written to the log, for this machine only"
	}

	if c.Mail.Host != "" {
		mail = fmt.Sprintf("%s:%d, from %s", c.Mail.Host, c.Mail.Port, c.Mail.From)
	}

	bootstrap := "no secret"
	if c.Auth.BootstrapSecret != "" {
		bootstrap = "secret set"
	}

	fmt.Fprintf(&b, "outgoing mail  %s\n", mail)
	fmt.Fprintf(&b, "bootstrap      %s\n", bootstrap)

	return b.String()
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
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
