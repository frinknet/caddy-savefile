// Package config defines the storage boundaries shared by the savefile
// directives and exposes them to every other package in the suite.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"

	"github.com/frinknet/caddy-savefile/pkg/paths"
)

const (
	// DefaultRootDir is the base directory used when root_dir is not set.
	DefaultRootDir = "./uploads"
	// DefaultMaxSize is the per-file limit used when max_size is not set.
	DefaultMaxSize = int64(10 << 20)
	// DefaultMaxParts is the part-count limit used when max_parts is not set.
	DefaultMaxParts = 100
	// DefaultVerifyMime is how strictly a declared type is believed.
	DefaultVerifyMime = paths.VerifyMimeOn
)

// Config holds the storage boundaries shared by the HTTP handlers and the
// template action.
type Config struct {
	RootDir    string `json:"root_dir,omitempty"`
	MaxSize    int64  `json:"max_size,omitempty"`
	MaxParts   int    `json:"max_parts,omitempty"`
	TempDir    string `json:"temp_dir,omitempty"`
	VerifyMime string `json:"verify_mime,omitempty"`
}

// Defaults returns the configuration used when the global block is absent.
func Defaults() Config {
	return Config{
		RootDir:    DefaultRootDir,
		MaxSize:    DefaultMaxSize,
		MaxParts:   DefaultMaxParts,
		TempDir:    os.TempDir(),
		VerifyMime: DefaultVerifyMime,
	}
}

var (
	mu     sync.RWMutex
	global = Defaults()
)

// Global returns a copy of the configuration parsed from the global options
// block. Handlers reach the global block through Helper.Option when adapting a
// Caddyfile; the templates extension is handed a bare dispenser and falls back
// to this.
func Global() Config {
	mu.RLock()
	defer mu.RUnlock()
	return global
}

// Store replaces the package-wide configuration.
func Store(cfg Config) {
	mu.Lock()
	global = cfg
	mu.Unlock()
}

// MaxSizeOr returns size when positive, otherwise the configured default.
func MaxSizeOr(size int64) int64 {
	if size > 0 {
		return size
	}
	return Global().MaxSize
}

func init() {
	httpcaddyfile.RegisterGlobalOption("savefile", parseGlobalOption)
}

func parseGlobalOption(d *caddyfile.Dispenser, existing any) (any, error) {
	cfg := Defaults()
	switch prev := existing.(type) {
	case *Config:
		if prev != nil {
			cfg = *prev
		}
	case Config:
		cfg = prev
	}

	if !d.Next() {
		return nil, d.ArgErr()
	}

	for d.NextBlock(0) {
		switch d.Val() {
		case "root_dir":
			if !d.Args(&cfg.RootDir) {
				return nil, d.ArgErr()
			}
		case "max_size":
			var raw string
			if !d.Args(&raw) {
				return nil, d.ArgErr()
			}
			size, err := ParseSize(raw)
			if err != nil {
				return nil, d.Errf("parsing max_size: %v", err)
			}
			cfg.MaxSize = size
		case "max_parts":
			var raw string
			if !d.Args(&raw) {
				return nil, d.ArgErr()
			}
			parts, err := strconv.Atoi(raw)
			if err != nil || parts <= 0 {
				return nil, d.Errf("max_parts must be a positive integer, got %q", raw)
			}
			cfg.MaxParts = parts
		case "temp_dir":
			if !d.Args(&cfg.TempDir) {
				return nil, d.ArgErr()
			}
		case "verify_mime":
			if !d.Args(&cfg.VerifyMime) {
				return nil, d.ArgErr()
			}
			if !paths.ValidVerifyMime(cfg.VerifyMime) {
				return nil, d.Errf("verify_mime must be one of %s, %s, %s, got %q",
					paths.VerifyMimeOn, paths.VerifyMimeStrict, paths.VerifyMimeOff, cfg.VerifyMime)
			}
		default:
			return nil, d.Errf("unrecognized savefile option: %s", d.Val())
		}
	}

	Store(cfg)
	return &cfg, nil
}

// ParseSize parses a byte size such as "50MB", "1GB" or a bare byte count.
// Suffixes are binary multiples: 1KB is 1024 bytes.
func ParseSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("empty size")
	}

	digits := 0
	for digits < len(s) && (s[digits] == '.' || (s[digits] >= '0' && s[digits] <= '9')) {
		digits++
	}
	num, unit := s[:digits], strings.ToLower(strings.TrimSpace(s[digits:]))
	if num == "" {
		return 0, fmt.Errorf("invalid size %q", raw)
	}

	value, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", raw, err)
	}

	var multiplier float64
	switch unit {
	case "", "b", "byte", "bytes":
		multiplier = 1
	case "k", "kb", "kib":
		multiplier = 1 << 10
	case "m", "mb", "mib":
		multiplier = 1 << 20
	case "g", "gb", "gib":
		multiplier = 1 << 30
	case "t", "tb", "tib":
		multiplier = 1 << 40
	default:
		return 0, fmt.Errorf("unknown size unit %q", unit)
	}

	total := value * multiplier
	if total <= 0 {
		return 0, fmt.Errorf("size must be positive: %q", raw)
	}
	return int64(total), nil
}
