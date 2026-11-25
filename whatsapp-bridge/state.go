package main

import (
	"sync"
	"time"
)

// Connection states shown in /api/status and on the dashboard.
const (
	StateStarting     = "starting"
	StateWaitingQR    = "waiting_for_qr"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
	StateLoggedOut    = "logged_out"
)

// BridgeState is the live status of the bridge, safe for concurrent use.
type BridgeState struct {
	mu             sync.RWMutex
	state          string
	qrCode         string
	qrExpires      time.Time
	lastError      string
	startedAt      time.Time
	connectedAt    *time.Time
	lastMessageAt  *time.Time
	receivedCount  int64
	sentCount      int64
	historyCount   int64
	lastHistoryAt  *time.Time
	reconnectCount int64
}

// NewBridgeState creates the initial state.
func NewBridgeState() *BridgeState {
	return &BridgeState{state: StateStarting, startedAt: time.Now()}
}

func (s *BridgeState) set(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == StateConnected {
		now := time.Now()
		if s.connectedAt != nil {
			s.reconnectCount++
		}
		s.connectedAt = &now
		s.qrCode = ""
		s.lastError = ""
	}
	s.state = state
}

func (s *BridgeState) setQR(code string, timeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StateWaitingQR
	s.qrCode = code
	s.qrExpires = time.Now().Add(timeout)
}

func (s *BridgeState) setError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err
}

func (s *BridgeState) noteMessage() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.lastMessageAt = &now
	s.receivedCount++
}

func (s *BridgeState) noteSent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sentCount++
}

func (s *BridgeState) noteHistory(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.lastHistoryAt = &now
	s.historyCount += int64(n)
}

// QR returns the current pairing code, if any.
func (s *BridgeState) QR() (string, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.qrCode, s.qrExpires
}

// Snapshot is the JSON form of the state.
type Snapshot struct {
	State          string     `json:"state"`
	Connected      bool       `json:"connected"`
	HasQR          bool       `json:"has_qr"`
	QRExpiresAt    *time.Time `json:"qr_expires_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	UptimeSeconds  int64      `json:"uptime_seconds"`
	ConnectedAt    *time.Time `json:"connected_at,omitempty"`
	LastMessageAt  *time.Time `json:"last_message_at,omitempty"`
	ReceivedCount  int64      `json:"received_since_start"`
	SentCount      int64      `json:"sent_since_start"`
	HistoryCount   int64      `json:"history_messages_since_start"`
	LastHistoryAt  *time.Time `json:"last_history_sync_at,omitempty"`
	ReconnectCount int64      `json:"reconnects"`
}

// Snapshot copies the state.
func (s *BridgeState) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := Snapshot{
		State: s.state, Connected: s.state == StateConnected, HasQR: s.qrCode != "", LastError: s.lastError,
		StartedAt: s.startedAt, UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		ConnectedAt: s.connectedAt, LastMessageAt: s.lastMessageAt, ReceivedCount: s.receivedCount,
		SentCount: s.sentCount, HistoryCount: s.historyCount, LastHistoryAt: s.lastHistoryAt, ReconnectCount: s.reconnectCount,
	}
	if s.qrCode != "" {
		t := s.qrExpires
		snap.QRExpiresAt = &t
	}
	return snap
}
