package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// maxUploadBytes is WhatsApp's practical limit for documents (2 GB) – far above
// anything that should be read into memory, so we stop earlier.
const maxUploadBytes = 100 << 20

// ErrNotConnected is returned when an action needs a live connection.
var ErrNotConnected = errors.New("not connected to WhatsApp")

// SendRequest is the body of POST /api/send.
type SendRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	MediaPath string `json:"media_path,omitempty"`
	// ReplyTo is the ID of a message in the same chat to quote.
	ReplyTo string `json:"reply_to,omitempty"`
}

// SendResult is returned after a successful send.
type SendResult struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	ChatJID   string    `json:"chat_jid"`
}

// parseRecipient accepts a phone number ("41 78 123 45 67", "+41781234567")
// or a full JID and returns the JID to send to.
func parseRecipient(recipient string) (types.JID, error) {
	r := strings.TrimSpace(recipient)
	if r == "" {
		return types.JID{}, errors.New("recipient is required")
	}
	if strings.Contains(r, "@") {
		jid, err := types.ParseJID(r)
		if err != nil {
			return types.JID{}, fmt.Errorf("invalid JID %q: %w", r, err)
		}
		return jid, nil
	}
	var digits strings.Builder
	for _, c := range r {
		switch {
		case c >= '0' && c <= '9':
			digits.WriteRune(c)
		case c == '+' || c == ' ' || c == '-' || c == '(' || c == ')' || c == '.':
			// formatting characters people commonly type
		default:
			return types.JID{}, fmt.Errorf("invalid phone number %q: use the international format without +, e.g. 41781234567", r)
		}
	}
	d := strings.TrimPrefix(digits.String(), "00")
	if len(d) < 7 || len(d) > 15 {
		return types.JID{}, fmt.Errorf("invalid phone number %q: expected 7-15 digits including the country code", r)
	}
	return types.NewJID(d, types.DefaultUserServer), nil
}

// buildQuote creates the context info that makes a message a reply.
func (a *App) buildQuote(chat types.JID, replyTo string) (*waProto.ContextInfo, error) {
	if replyTo == "" {
		return nil, nil
	}
	m, err := a.store.GetMessage(chat.String(), replyTo)
	if err != nil {
		return nil, fmt.Errorf("message %s to reply to was not found in chat %s", replyTo, chat.String())
	}
	var participant string
	if m.IsFromMe && a.client.Store.ID != nil {
		participant = a.client.Store.ID.ToNonAD().String()
	} else if m.Sender != "" {
		participant = types.NewJID(m.Sender, types.DefaultUserServer).String()
	} else {
		participant = chat.String()
	}
	quoted := m.Content
	if quoted == "" && m.MediaType != "" {
		quoted = "[" + m.MediaType + "]"
	}
	return &waProto.ContextInfo{
		StanzaID:      proto.String(replyTo),
		Participant:   proto.String(participant),
		QuotedMessage: &waProto.Message{Conversation: proto.String(quoted)},
	}, nil
}

// send delivers a text or media message.
func (a *App) send(ctx context.Context, req SendRequest) (SendResult, error) {
	if !a.client.IsConnected() {
		return SendResult{}, ErrNotConnected
	}
	jid, err := parseRecipient(req.Recipient)
	if err != nil {
		return SendResult{}, err
	}
	if req.Message == "" && req.MediaPath == "" {
		return SendResult{}, errors.New("message or media_path is required")
	}
	quote, err := a.buildQuote(jid, req.ReplyTo)
	if err != nil {
		return SendResult{}, err
	}

	msg := &waProto.Message{}
	if req.MediaPath != "" {
		msg, err = a.buildMediaMessage(ctx, req.MediaPath, req.Message, quote)
		if err != nil {
			return SendResult{}, err
		}
	} else if quote != nil {
		msg.ExtendedTextMessage = &waProto.ExtendedTextMessage{Text: proto.String(req.Message), ContextInfo: quote}
	} else {
		msg.Conversation = proto.String(req.Message)
	}

	resp, err := a.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return SendResult{}, fmt.Errorf("error sending message: %w", err)
	}
	// Store our own message right away so it shows up in the history immediately.
	content := req.Message
	media := extractMediaInfo(msg, resp.ID)
	if a.store.ChatName(jid.String()) == "" {
		a.store.StoreChat(jid.String(), a.chatName(ctx, jid, jid.String(), nil, jid.User), resp.Timestamp)
	} else {
		a.store.StoreChat(jid.String(), "", resp.Timestamp)
	}
	own := ""
	if a.client.Store.ID != nil {
		own = a.client.Store.ID.User
	}
	a.store.StoreMessage(StoredMessage{
		ID: resp.ID, ChatJID: jid.String(), Sender: own, Content: content, Timestamp: resp.Timestamp, IsFromMe: true,
		MediaType: media.Type, Filename: media.Filename, URL: media.URL, MediaKey: media.MediaKey,
		FileSHA256: media.FileSHA256, FileEncSHA256: media.FileEncSHA256, FileLength: media.FileLength, QuotedID: req.ReplyTo,
	})
	a.state.noteSent()
	return SendResult{ID: resp.ID, Timestamp: resp.Timestamp, ChatJID: jid.String()}, nil
}

func (a *App) buildMediaMessage(ctx context.Context, path, caption string, quote *waProto.ContextInfo) (*waProto.Message, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("error reading media file: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("media path is a directory: %s", path)
	}
	if st.Size() > maxUploadBytes {
		return nil, fmt.Errorf("file is too large (%d MB, limit %d MB)", st.Size()>>20, maxUploadBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading media file: %w", err)
	}
	mediaType, mimeType := detectMedia(path, data)
	resp, err := a.client.Upload(ctx, data, mediaType)
	if err != nil {
		return nil, fmt.Errorf("error uploading media: %w", err)
	}
	msg := &waProto.Message{}
	switch mediaType {
	case whatsmeow.MediaImage:
		msg.ImageMessage = &waProto.ImageMessage{
			Caption: optString(caption), Mimetype: proto.String(mimeType), URL: &resp.URL, DirectPath: &resp.DirectPath,
			MediaKey: resp.MediaKey, FileEncSHA256: resp.FileEncSHA256, FileSHA256: resp.FileSHA256, FileLength: &resp.FileLength,
			ContextInfo: quote,
		}
	case whatsmeow.MediaAudio:
		seconds, waveform, err := analyzeOggOpus(data)
		if err != nil {
			return nil, fmt.Errorf("failed to analyze Ogg Opus file: %w", err)
		}
		msg.AudioMessage = &waProto.AudioMessage{
			Mimetype: proto.String(mimeType), URL: &resp.URL, DirectPath: &resp.DirectPath,
			MediaKey: resp.MediaKey, FileEncSHA256: resp.FileEncSHA256, FileSHA256: resp.FileSHA256, FileLength: &resp.FileLength,
			Seconds: proto.Uint32(seconds), PTT: proto.Bool(true), Waveform: waveform, ContextInfo: quote,
		}
	case whatsmeow.MediaVideo:
		msg.VideoMessage = &waProto.VideoMessage{
			Caption: optString(caption), Mimetype: proto.String(mimeType), URL: &resp.URL, DirectPath: &resp.DirectPath,
			MediaKey: resp.MediaKey, FileEncSHA256: resp.FileEncSHA256, FileSHA256: resp.FileSHA256, FileLength: &resp.FileLength,
			ContextInfo: quote,
		}
	default:
		name := filepath.Base(path)
		msg.DocumentMessage = &waProto.DocumentMessage{
			Title: proto.String(name), FileName: proto.String(name), Caption: optString(caption), Mimetype: proto.String(mimeType),
			URL: &resp.URL, DirectPath: &resp.DirectPath, MediaKey: resp.MediaKey, FileEncSHA256: resp.FileEncSHA256,
			FileSHA256: resp.FileSHA256, FileLength: &resp.FileLength, ContextInfo: quote,
		}
	}
	return msg, nil
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return proto.String(s)
}

// react adds (or with an empty emoji removes) a reaction to a message.
func (a *App) react(ctx context.Context, chatJID, messageID, emoji string) error {
	if !a.client.IsConnected() {
		return ErrNotConnected
	}
	chat, err := parseRecipient(chatJID)
	if err != nil {
		return err
	}
	m, err := a.store.GetMessage(chat.String(), messageID)
	if err != nil {
		return fmt.Errorf("message %s not found in chat %s", messageID, chat.String())
	}
	sender := chat
	if m.IsFromMe && a.client.Store.ID != nil {
		sender = a.client.Store.ID.ToNonAD()
	} else if m.Sender != "" {
		sender = types.NewJID(m.Sender, types.DefaultUserServer)
	}
	_, err = a.client.SendMessage(ctx, chat, a.client.BuildReaction(chat, sender, messageID, emoji))
	if err == nil {
		own := ""
		if a.client.Store.ID != nil {
			own = a.client.Store.ID.User
		}
		a.store.StoreReaction(chat.String(), messageID, own, emoji, time.Now())
	}
	return err
}

// markRead sends read receipts for messages in a chat (the blue ticks).
func (a *App) markRead(ctx context.Context, chatJID string, ids []string) (int, error) {
	if !a.client.IsConnected() {
		return 0, ErrNotConnected
	}
	chat, err := parseRecipient(chatJID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		rows, err := a.store.db.Query("SELECT id FROM messages WHERE chat_jid = ? AND is_from_me = 0 ORDER BY timestamp DESC LIMIT 20", chat.String())
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// Receipts in groups must name the sender; send them per sender.
	bySender := map[string][]types.MessageID{}
	for _, id := range ids {
		sender := ""
		if m, err := a.store.GetMessage(chat.String(), id); err == nil && !m.IsFromMe {
			sender = m.Sender
		}
		bySender[sender] = append(bySender[sender], id)
	}
	n := 0
	for sender, list := range bySender {
		senderJID := types.EmptyJID
		if chat.Server == types.GroupServer && sender != "" {
			senderJID = types.NewJID(sender, types.DefaultUserServer)
		}
		if err := a.client.MarkRead(ctx, list, time.Now(), chat, senderJID); err != nil {
			return n, err
		}
		n += len(list)
	}
	return n, nil
}

// setTyping shows or hides "typing…" in a chat.
func (a *App) setTyping(ctx context.Context, chatJID string, typing bool) error {
	if !a.client.IsConnected() {
		return ErrNotConnected
	}
	chat, err := parseRecipient(chatJID)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if typing {
		state = types.ChatPresenceComposing
	}
	return a.client.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText)
}
