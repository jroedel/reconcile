module github.com/jroedel/reconcile

// One directive, and no separate `toolchain` line, for the reason
// mass-intentions gives: with both, CI installs the first and then downloads
// the second into the module cache on every run.
//
// The patch version is pinned deliberately. actions/setup-go reads this line,
// so CI builds with exactly this toolchain; `go 1.26` would mean the first 1.26
// release, and govulncheck would then report every standard library advisory
// fixed since -- red in CI and green on a developer machine.
//
// Raise this when a Go patch release fixes an advisory the scan reports.
go 1.26.8

require github.com/BurntSushi/toml v1.6.0

require (
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/telemetry v0.0.0-20260908163034-4bcc4b2ee518 // indirect
	golang.org/x/tools v0.50.0 // indirect
	golang.org/x/vuln v1.8.0 // indirect
)

tool golang.org/x/vuln/cmd/govulncheck
