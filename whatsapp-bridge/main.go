// Command whatsapp-bridge connects to your personal WhatsApp account through
// the multi-device web API, keeps a local SQLite history for the MCP server and
// exposes a small, locked-down REST API plus a status dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if a bridge is running on WHATSAPP_BRIDGE_ADDR (for Docker)")
	flag.Parse()
	if *showVersion {
		fmt.Println("whatsapp-bridge", Version)
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck(LoadConfig()))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := LoadConfig()
	if err := cfg.Validate(); err != nil {
		return err
	}
	logger := waLog.Stdout("Client", cfg.LogLevel, true)
	logger.Infof("Starting WhatsApp bridge %s", Version)

	if err := os.MkdirAll(cfg.StoreDir, 0o700); err != nil {
		return fmt.Errorf("failed to create store directory: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbLog := waLog.Stdout("Database", "WARN", true)
	sessionDSN := "file:" + filepath.ToSlash(filepath.Join(cfg.StoreDir, "whatsapp.db")) + "?_foreign_keys=on&_busy_timeout=5000"
	container, err := sqlstore.New(ctx, "sqlite3", sessionDSN, dbLog)
	if err != nil {
		return fmt.Errorf("failed to open session database: %w", err)
	}
	messageStore, err := NewMessageStore(cfg.StoreDir)
	if err != nil {
		return err
	}
	defer messageStore.Close()

	app := &App{cfg: cfg, store: messageStore, log: logger, state: NewBridgeState()}

	// Start the API first so the dashboard can show the QR code during pairing.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (is another bridge already running?): %w", cfg.Addr, err)
	}
	srv := &http.Server{Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Errorf("REST API server error: %v", err)
		}
	}()
	fmt.Printf("Dashboard and REST API: http://%s/\n", cfg.Addr)
	if cfg.Token != "" {
		fmt.Println("API token protection is ON (open the dashboard with ?token=<your token> once).")
	}

	go app.runScheduler(ctx, 15*time.Second)

	for ctx.Err() == nil {
		loggedOut, err := app.session(ctx, container)
		if err != nil {
			app.state.set(StateDisconnected)
			app.state.setError(err.Error())
			logger.Errorf("%v", err)
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
			continue
		}
		if !loggedOut {
			break
		}
		fmt.Println("\nThis device was logged out from WhatsApp. Starting a new pairing…")
	}

	fmt.Println("Shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	if app.client != nil {
		app.client.Disconnect()
	}
	return nil
}

// runHealthcheck queries /api/health of a running bridge.
func runHealthcheck(cfg Config) int {
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/health")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// session runs one WhatsApp login until shutdown (false) or logout (true).
func (app *App) session(ctx context.Context, container *sqlstore.Container) (loggedOut bool, err error) {
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to load device: %w", err)
	}
	client := whatsmeow.NewClient(device, app.log)
	app.client = client

	logoutCh := make(chan struct{}, 1)
	client.AddEventHandler(func(evt any) {
		switch v := evt.(type) {
		case *events.Message:
			app.handleMessage(v)
		case *events.HistorySync:
			app.handleHistorySync(v)
		case *events.PushName:
			app.handlePushName(v)
		case *events.GroupInfo:
			app.handleGroupInfo(v)
		case *events.Contact:
			app.handleContact(v)
		case *events.Connected:
			app.state.set(StateConnected)
			fmt.Println("✓ Connected to WhatsApp")
		case *events.Disconnected:
			app.state.set(StateDisconnected)
		case *events.KeepAliveTimeout:
			app.state.setError("keep-alive timeout, reconnecting")
		case *events.StreamReplaced:
			app.state.set(StateDisconnected)
			app.state.setError("another bridge instance connected with the same session")
		case *events.TemporaryBan:
			app.state.set(StateDisconnected)
			app.state.setError("WhatsApp temporarily banned this session: " + v.String())
		case *events.LoggedOut:
			app.state.set(StateLoggedOut)
			select {
			case logoutCh <- struct{}{}:
			default:
			}
		}
	})

	if client.Store.ID == nil {
		if err := app.pair(ctx, client); err != nil {
			client.Disconnect()
			return false, err
		}
	} else {
		app.state.set(StateConnecting)
		if err := client.Connect(); err != nil {
			return false, fmt.Errorf("failed to connect: %w", err)
		}
	}

	select {
	case <-ctx.Done():
		return false, nil
	case <-logoutCh:
		client.Disconnect()
		if err := device.Delete(context.Background()); err != nil {
			app.log.Warnf("Failed to delete old device data: %v", err)
		}
		return true, nil
	}
}

// pair shows QR codes (terminal + dashboard) until the phone links this device.
// It never gives up on its own: expired codes are replaced automatically.
func (app *App) pair(ctx context.Context, client *whatsmeow.Client) error {
	for ctx.Err() == nil {
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("failed to start pairing: %w", err)
		}
		if err := client.Connect(); err != nil {
			return fmt.Errorf("failed to connect: %w", err)
		}
		for evt := range qrChan {
			switch evt.Event {
			case whatsmeow.QRChannelEventCode:
				app.state.setQR(evt.Code, evt.Timeout)
				fmt.Printf("\nScan this QR code in WhatsApp → Settings → Linked devices → Link a device\n(or open http://%s/ in your browser)\n", app.cfg.Addr)
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			case whatsmeow.QRChannelSuccess.Event:
				fmt.Println("\n✓ Paired successfully! Your history will sync in the background.")
				app.state.set(StateConnecting)
				return nil
			case whatsmeow.QRChannelTimeout.Event:
				app.log.Infof("QR codes expired, generating new ones")
			case whatsmeow.QRChannelEventError:
				app.state.setError(fmt.Sprintf("pairing error: %v", evt.Error))
			default:
				app.state.setError("pairing: " + evt.Event)
			}
		}
		client.Disconnect()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return ctx.Err()
}
