package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime settings of the bridge. Every value can be set
// through an environment variable so the bridge works the same way when run
// by hand, as a Windows autostart entry or inside Docker.
type Config struct {
	// Addr is the host:port the REST API and dashboard listen on.
	// Defaults to 127.0.0.1:8080 so the API is never exposed to the LAN by accident.
	Addr string
	// Token is an optional shared secret. When set, every API call must send it
	// (Authorization: Bearer <token>, X-Bridge-Token header or the dashboard cookie).
	Token string
	// StoreDir is where whatsapp.db (session), messages.db and downloaded media live.
	StoreDir string
	// LogContent prints message text to the log. Off by default: logs are often
	// written to files (bridge.log) and should not contain private messages.
	LogContent bool
	// LogLevel for whatsmeow: DEBUG, INFO, WARN or ERROR.
	LogLevel string
	// AllowInsecure permits a non-loopback Addr without a Token. Only for
	// trusted, isolated networks (for example a private Docker network).
	AllowInsecure bool
	// HistoryRequestCount is how many messages per chat to ask for on startup history sync requests.
	HistoryRequestCount int
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// LoadConfig reads the configuration from the environment.
func LoadConfig() Config {
	addr := envOr("WHATSAPP_BRIDGE_ADDR", "")
	if addr == "" {
		// Backwards compatible: PORT alone keeps the loopback host.
		addr = "127.0.0.1:" + envOr("WHATSAPP_BRIDGE_PORT", "8080")
	}
	return Config{
		Addr:                addr,
		Token:               envOr("WHATSAPP_BRIDGE_TOKEN", ""),
		StoreDir:            envOr("WHATSAPP_STORE_DIR", "store"),
		LogContent:          envBool("WHATSAPP_LOG_CONTENT", false),
		LogLevel:            strings.ToUpper(envOr("WHATSAPP_LOG_LEVEL", "INFO")),
		AllowInsecure:       envBool("WHATSAPP_BRIDGE_ALLOW_INSECURE", false),
		HistoryRequestCount: envInt("WHATSAPP_HISTORY_REQUEST_COUNT", 100),
	}
}

// IsLoopback reports whether the configured address only listens locally.
func (c Config) IsLoopback() bool {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate refuses configurations that would expose the account without protection.
func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("invalid WHATSAPP_BRIDGE_ADDR %q: %w", c.Addr, err)
	}
	if !c.IsLoopback() && c.Token == "" && !c.AllowInsecure {
		return fmt.Errorf("refusing to listen on %s without WHATSAPP_BRIDGE_TOKEN: anyone on the network could send messages as you. "+
			"Set a token, bind to 127.0.0.1, or set WHATSAPP_BRIDGE_ALLOW_INSECURE=1 if the network is isolated", c.Addr)
	}
	switch c.LogLevel {
	case "DEBUG", "INFO", "WARN", "ERROR":
	default:
		return fmt.Errorf("invalid WHATSAPP_LOG_LEVEL %q (use DEBUG, INFO, WARN or ERROR)", c.LogLevel)
	}
	return nil
}
