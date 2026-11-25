package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

//go:embed dashboard.html
var dashboardHTML []byte

// Version is set at build time with -ldflags "-X main.Version=v1.2.3".
var Version = "dev"

const tokenCookie = "wa_bridge_token"

// maxBodyBytes limits JSON request bodies.
const maxBodyBytes = 1 << 20

type apiError struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Success: false, Message: msg})
}

// statusFor maps internal errors to HTTP status codes.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrNotConnected):
		return http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	}
	return http.StatusInternalServerError
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// Security middleware -------------------------------------------------------

// isLocalHostHeader accepts only Host headers that point at this machine,
// which blocks DNS-rebinding attacks from websites in the user's browser.
func isLocalHostHeader(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (a *App) tokenOK(r *http.Request) bool {
	if a.cfg.Token == "" {
		return true
	}
	candidates := []string{r.Header.Get("X-Bridge-Token")}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		candidates = append(candidates, strings.TrimPrefix(auth, "Bearer "))
	}
	if c, err := r.Cookie(tokenCookie); err == nil {
		candidates = append(candidates, c.Value)
	}
	for _, c := range candidates {
		if c != "" && subtle.ConstantTimeCompare([]byte(c), []byte(a.cfg.Token)) == 1 {
			return true
		}
	}
	return false
}

// secure wraps every handler:
//   - Host check (DNS rebinding) when bound to loopback,
//   - Origin check: browsers on other sites cannot call the API (CSRF),
//   - JSON-only POST bodies, so plain HTML forms cannot submit,
//   - optional token.
func (a *App) secure(public bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if a.cfg.IsLoopback() && !isLocalHostHeader(r.Host) {
			writeErr(w, http.StatusForbidden, "host not allowed")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if origin != "http://"+r.Host && origin != "https://"+r.Host {
				writeErr(w, http.StatusForbidden, "cross-origin requests are not allowed")
				return
			}
		}
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			if r.Method == http.MethodPost {
				mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if mt != "application/json" {
					writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
					return
				}
			}
		}
		if !public && !a.tokenOK(r) {
			writeErr(w, http.StatusUnauthorized, "missing or wrong bridge token")
			return
		}
		next(w, r)
	}
}

// Handlers ------------------------------------------------------------------

func (a *App) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.secure(true, a.handleDashboard))
	mux.HandleFunc("GET /api/health", a.secure(true, a.handleHealth))
	mux.HandleFunc("GET /api/status", a.secure(false, a.handleStatus))
	mux.HandleFunc("GET /api/qr.svg", a.secure(false, a.handleQRSVG))
	mux.HandleFunc("POST /api/send", a.secure(false, a.handleSend))
	mux.HandleFunc("POST /api/download", a.secure(false, a.handleDownload))
	mux.HandleFunc("POST /api/react", a.secure(false, a.handleReact))
	mux.HandleFunc("POST /api/read", a.secure(false, a.handleRead))
	mux.HandleFunc("POST /api/typing", a.secure(false, a.handleTyping))
	mux.HandleFunc("POST /api/check", a.secure(false, a.handleCheck))
	mux.HandleFunc("POST /api/history", a.secure(false, a.handleHistory))
	mux.HandleFunc("GET /api/groups", a.secure(false, a.handleGroups))
	mux.HandleFunc("GET /api/group", a.secure(false, a.handleGroup))
	mux.HandleFunc("GET /api/schedule", a.secure(false, a.handleListSchedule))
	mux.HandleFunc("POST /api/schedule", a.secure(false, a.handleAddSchedule))
	mux.HandleFunc("DELETE /api/schedule/{id}", a.secure(false, a.handleCancelSchedule))
	return mux
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	// /?token=... stores the token in a strict, HttpOnly cookie so the dashboard can
	// call the API without the token appearing in later URLs.
	if t := r.URL.Query().Get("token"); t != "" && a.cfg.Token != "" &&
		subtle.ConstantTimeCompare([]byte(t), []byte(a.cfg.Token)) == 1 {
		http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Write(dashboardHTML)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := a.state.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": snap.State, "connected": snap.Connected, "version": Version})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := a.state.Snapshot()
	chats, msgs, contacts, last := a.store.Stats()
	account := ""
	if a.client != nil && a.client.Store != nil && a.client.Store.ID != nil {
		account = a.client.Store.ID.User
	}
	pending, _ := a.store.ListScheduled("pending")
	writeJSON(w, http.StatusOK, map[string]any{
		"success":             true,
		"version":             Version,
		"bridge":              snap,
		"account":             account,
		"token_required":      a.cfg.Token != "",
		"listen":              a.cfg.Addr,
		"chats":               chats,
		"messages":            msgs,
		"contacts":            contacts,
		"last_message_time":   last,
		"scheduled_pending":   len(pending),
		"log_message_content": a.cfg.LogContent,
	})
}

func (a *App) handleQRSVG(w http.ResponseWriter, r *http.Request) {
	code, _ := a.state.QR()
	if code == "" {
		writeErr(w, http.StatusNotFound, "no QR code right now (already paired or still starting)")
		return
	}
	svg, err := qrSVG(code)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(svg))
}

func (a *App) handleSend(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Recipient) == "" {
		writeErr(w, http.StatusBadRequest, "Recipient is required")
		return
	}
	if req.Message == "" && req.MediaPath == "" {
		writeErr(w, http.StatusBadRequest, "Message or media path is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := a.send(ctx, req)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": "Message sent to " + req.Recipient,
		"id": res.ID, "chat_jid": res.ChatJID, "timestamp": res.Timestamp,
	})
}

func (a *App) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MessageID string `json:"message_id"`
		ChatJID   string `json:"chat_jid"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.MessageID == "" || req.ChatJID == "" {
		writeErr(w, http.StatusBadRequest, "Message ID and Chat JID are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := a.downloadMedia(ctx, req.MessageID, req.ChatJID)
	if err != nil {
		writeErr(w, statusFor(err), "Failed to download media: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": "Successfully downloaded " + res.MediaType + " media",
		"filename": res.Filename, "path": res.Path, "media_type": res.MediaType, "cached": res.Cached,
	})
}

func (a *App) handleReact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID   string `json:"chat_jid"`
		MessageID string `json:"message_id"`
		Emoji     string `json:"emoji"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ChatJID == "" || req.MessageID == "" {
		writeErr(w, http.StatusBadRequest, "chat_jid and message_id are required")
		return
	}
	if err := a.react(r.Context(), req.ChatJID, req.MessageID, req.Emoji); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	msg := "Reaction sent"
	if req.Emoji == "" {
		msg = "Reaction removed"
	}
	writeJSON(w, http.StatusOK, apiError{Success: true, Message: msg})
}

func (a *App) handleRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID    string   `json:"chat_jid"`
		MessageIDs []string `json:"message_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ChatJID == "" {
		writeErr(w, http.StatusBadRequest, "chat_jid is required")
		return
	}
	n, err := a.markRead(r.Context(), req.ChatJID, req.MessageIDs)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": fmt.Sprintf("Marked %d message(s) as read", n), "count": n})
}

func (a *App) handleTyping(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID string `json:"chat_jid"`
		Typing  bool   `json:"typing"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.setTyping(r.Context(), req.ChatJID, req.Typing); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apiError{Success: true, Message: "ok"})
}

func (a *App) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phones []string `json:"phones"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Phones) == 0 || len(req.Phones) > 50 {
		writeErr(w, http.StatusBadRequest, "send between 1 and 50 phone numbers")
		return
	}
	if !a.client.IsConnected() {
		writeErr(w, http.StatusServiceUnavailable, ErrNotConnected.Error())
		return
	}
	phones := make([]string, 0, len(req.Phones))
	for _, p := range req.Phones {
		jid, err := parseRecipient(p)
		if err != nil || jid.Server != types.DefaultUserServer {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid phone number: %s", p))
			return
		}
		phones = append(phones, "+"+jid.User)
	}
	res, err := a.client.IsOnWhatsApp(r.Context(), phones)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	out := make([]map[string]any, 0, len(res))
	for _, x := range res {
		out = append(out, map[string]any{"query": x.Query, "on_whatsapp": x.IsIn, "jid": x.JID.String()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "results": out})
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID string `json:"chat_jid"`
		Count   int    `json:"count"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	chat, err := parseRecipient(req.ChatJID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Count <= 0 || req.Count > 500 {
		req.Count = a.cfg.HistoryRequestCount
	}
	if err := a.requestHistorySync(r.Context(), chat, req.Count); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apiError{Success: true, Message: "History requested from your phone. Older messages arrive in the background if your phone is online."})
}

type groupJSON struct {
	JID          string        `json:"jid"`
	Name         string        `json:"name"`
	Topic        string        `json:"topic,omitempty"`
	Created      time.Time     `json:"created,omitempty"`
	Participants []participant `json:"participants,omitempty"`
	Size         int           `json:"size"`
	IsAnnounce   bool          `json:"announce_only"`
	IsLocked     bool          `json:"locked"`
}

type participant struct {
	JID          string `json:"jid"`
	Phone        string `json:"phone,omitempty"`
	IsAdmin      bool   `json:"is_admin"`
	IsSuperAdmin bool   `json:"is_super_admin"`
}

func toGroupJSON(g *types.GroupInfo, withParticipants bool) groupJSON {
	out := groupJSON{JID: g.JID.String(), Name: g.Name, Topic: g.Topic, Created: g.GroupCreated, Size: len(g.Participants),
		IsAnnounce: g.IsAnnounce, IsLocked: g.IsLocked}
	if g.ParticipantCount > out.Size {
		out.Size = g.ParticipantCount
	}
	if withParticipants {
		for _, p := range g.Participants {
			phone := ""
			if !p.PhoneNumber.IsEmpty() {
				phone = p.PhoneNumber.User
			} else if p.JID.Server == types.DefaultUserServer {
				phone = p.JID.User
			}
			out.Participants = append(out.Participants, participant{JID: p.JID.String(), Phone: phone, IsAdmin: p.IsAdmin, IsSuperAdmin: p.IsSuperAdmin})
		}
	}
	return out
}

func (a *App) handleGroups(w http.ResponseWriter, r *http.Request) {
	if !a.client.IsConnected() {
		writeErr(w, http.StatusServiceUnavailable, ErrNotConnected.Error())
		return
	}
	groups, err := a.client.GetJoinedGroups(r.Context())
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	out := make([]groupJSON, 0, len(groups))
	for _, g := range groups {
		out = append(out, toGroupJSON(g, false))
		a.store.RenameChat(g.JID.String(), g.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "groups": out})
}

func (a *App) handleGroup(w http.ResponseWriter, r *http.Request) {
	jid, err := types.ParseJID(r.URL.Query().Get("jid"))
	if err != nil || jid.Server != types.GroupServer {
		writeErr(w, http.StatusBadRequest, "jid must be a group JID ending in @g.us")
		return
	}
	if !a.client.IsConnected() {
		writeErr(w, http.StatusServiceUnavailable, ErrNotConnected.Error())
		return
	}
	g, err := a.client.GetGroupInfo(r.Context(), jid)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "group": toGroupJSON(g, true)})
}

func (a *App) handleListSchedule(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "sent", "failed", "cancelled":
	default:
		writeErr(w, http.StatusBadRequest, "status must be pending, sent, failed or cancelled")
		return
	}
	list, err := a.store.ListScheduled(status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "scheduled": list})
}

func (a *App) handleAddSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SendRequest
		SendAt string `json:"send_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := parseRecipient(req.Recipient); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Message == "" && req.MediaPath == "" {
		writeErr(w, http.StatusBadRequest, "message or media_path is required")
		return
	}
	at, err := time.Parse(time.RFC3339, req.SendAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "send_at must be RFC 3339 with a time zone, e.g. 2026-12-01T09:15:00+01:00")
		return
	}
	if at.Before(time.Now().Add(-time.Minute)) {
		writeErr(w, http.StatusBadRequest, "send_at is in the past")
		return
	}
	if at.After(time.Now().AddDate(1, 0, 0)) {
		writeErr(w, http.StatusBadRequest, "send_at must be within one year")
		return
	}
	id, err := a.store.AddScheduled(ScheduledMessage{Recipient: req.Recipient, Message: req.Message, MediaPath: req.MediaPath, ReplyTo: req.ReplyTo, SendAt: at})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "id": id, "send_at": at.UTC(),
		"message": fmt.Sprintf("Scheduled #%d for %s. The bridge must be running at that time.", id, at.Format(time.RFC3339))})
}

func (a *App) handleCancelSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	ok, err := a.store.CancelScheduled(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no pending scheduled message #%d", id))
		return
	}
	writeJSON(w, http.StatusOK, apiError{Success: true, Message: fmt.Sprintf("Cancelled #%d", id)})
}
