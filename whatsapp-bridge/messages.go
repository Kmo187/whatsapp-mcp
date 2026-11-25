package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// mediaInfo is everything needed to download an attachment later.
type mediaInfo struct {
	Type          string
	Filename      string
	URL           string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
}

// unwrapMessage removes wrapper messages (ephemeral, view-once, edits in
// device-sent form) so the real payload can be inspected.
func unwrapMessage(msg *waProto.Message) *waProto.Message {
	for i := 0; i < 5 && msg != nil; i++ {
		switch {
		case msg.GetEphemeralMessage().GetMessage() != nil:
			msg = msg.GetEphemeralMessage().GetMessage()
		case msg.GetViewOnceMessage().GetMessage() != nil:
			msg = msg.GetViewOnceMessage().GetMessage()
		case msg.GetViewOnceMessageV2().GetMessage() != nil:
			msg = msg.GetViewOnceMessageV2().GetMessage()
		case msg.GetDocumentWithCaptionMessage().GetMessage() != nil:
			msg = msg.GetDocumentWithCaptionMessage().GetMessage()
		case msg.GetDeviceSentMessage().GetMessage() != nil:
			msg = msg.GetDeviceSentMessage().GetMessage()
		default:
			return msg
		}
	}
	return msg
}

// extractTextContent returns the text of a message, including media captions,
// locations, contacts and polls, so they become searchable.
func extractTextContent(msg *waProto.Message) string {
	msg = unwrapMessage(msg)
	if msg == nil {
		return ""
	}
	switch {
	case msg.GetConversation() != "":
		return msg.GetConversation()
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetText()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetCaption()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetCaption()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetCaption()
	case msg.GetLocationMessage() != nil:
		l := msg.GetLocationMessage()
		label := strings.TrimSpace(l.GetName() + " " + l.GetAddress())
		return strings.TrimSpace(fmt.Sprintf("[location] %s https://maps.google.com/?q=%f,%f", label, l.GetDegreesLatitude(), l.GetDegreesLongitude()))
	case msg.GetLiveLocationMessage() != nil:
		l := msg.GetLiveLocationMessage()
		return fmt.Sprintf("[live location] https://maps.google.com/?q=%f,%f", l.GetDegreesLatitude(), l.GetDegreesLongitude())
	case msg.GetContactMessage() != nil:
		return "[contact] " + msg.GetContactMessage().GetDisplayName()
	case msg.GetPollCreationMessage() != nil:
		return pollText(msg.GetPollCreationMessage())
	case msg.GetPollCreationMessageV3() != nil:
		return pollText(msg.GetPollCreationMessageV3())
	}
	return ""
}

func pollText(p *waProto.PollCreationMessage) string {
	opts := make([]string, 0, len(p.GetOptions()))
	for _, o := range p.GetOptions() {
		opts = append(opts, o.GetOptionName())
	}
	return fmt.Sprintf("[poll] %s (%s)", p.GetName(), strings.Join(opts, " / "))
}

// quotedID returns the ID of the message this one replies to, if any.
func quotedID(msg *waProto.Message) string {
	msg = unwrapMessage(msg)
	if msg == nil {
		return ""
	}
	for _, ci := range []*waProto.ContextInfo{
		msg.GetExtendedTextMessage().GetContextInfo(),
		msg.GetImageMessage().GetContextInfo(),
		msg.GetVideoMessage().GetContextInfo(),
		msg.GetAudioMessage().GetContextInfo(),
		msg.GetDocumentMessage().GetContextInfo(),
		msg.GetStickerMessage().GetContextInfo(),
	} {
		if id := ci.GetStanzaID(); id != "" {
			return id
		}
	}
	return ""
}

// extractMediaInfo returns download information for media messages.
// Filenames are derived from the message ID so they are unique and safe.
func extractMediaInfo(msg *waProto.Message, messageID string) mediaInfo {
	msg = unwrapMessage(msg)
	if msg == nil {
		return mediaInfo{}
	}
	base := safeFilename(messageID)
	if base == "" {
		base = time.Now().Format("20060102_150405")
	}
	if img := msg.GetImageMessage(); img != nil {
		return mediaInfo{"image", "image_" + base + extForMime(img.GetMimetype(), ".jpg"), img.GetURL(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()}
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return mediaInfo{"video", "video_" + base + extForMime(vid.GetMimetype(), ".mp4"), vid.GetURL(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()}
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return mediaInfo{"audio", "audio_" + base + extForMime(aud.GetMimetype(), ".ogg"), aud.GetURL(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()}
	}
	if st := msg.GetStickerMessage(); st != nil {
		return mediaInfo{"sticker", "sticker_" + base + ".webp", st.GetURL(), st.GetMediaKey(), st.GetFileSHA256(), st.GetFileEncSHA256(), st.GetFileLength()}
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		name := safeFilename(doc.GetFileName())
		if name == "" {
			name = "document_" + base + extForMime(doc.GetMimetype(), "")
		} else {
			// Prefix with the ID: two documents called "scan.pdf" must not overwrite each other.
			name = base + "_" + name
		}
		return mediaInfo{"document", name, doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()}
	}
	return mediaInfo{}
}

// App bundles the long-lived parts of the bridge.
type App struct {
	cfg    Config
	client *whatsmeow.Client
	store  *MessageStore
	log    waLog.Logger
	state  *BridgeState
}

// logMessage prints an incoming/outgoing message. Content is only shown when
// WHATSAPP_LOG_CONTENT is enabled.
func (a *App) logMessage(ts time.Time, fromMe bool, chat, sender, mediaType, content string) {
	dir := "←"
	if fromMe {
		dir = "→"
	}
	body := fmt.Sprintf("%d chars", len([]rune(content)))
	if a.cfg.LogContent {
		body = content
	}
	if mediaType != "" {
		body = "[" + mediaType + "] " + body
	}
	fmt.Printf("[%s] %s %s (%s): %s\n", ts.Format("2006-01-02 15:04:05"), dir, chat, sender, body)
}

// senderID returns the phone-number user part of the sender when available.
// WhatsApp increasingly uses anonymous LIDs; the phone number is far more useful
// for the MCP server because contacts and direct chats are keyed by it.
func (a *App) senderID(ctx context.Context, info types.MessageInfo) string {
	if info.Sender.Server == types.HiddenUserServer {
		if info.SenderAlt.Server == types.DefaultUserServer {
			return info.SenderAlt.User
		}
		if a.client != nil && a.client.Store != nil && a.client.Store.LIDs != nil {
			if pn, err := a.client.Store.LIDs.GetPNForLID(ctx, info.Sender.ToNonAD()); err == nil && !pn.IsEmpty() {
				return pn.User
			}
		}
	}
	return info.Sender.User
}

// chatJIDString normalises LID-addressed direct chats to the phone-number JID when known.
func (a *App) chatJIDString(ctx context.Context, chat types.JID) string {
	if chat.Server == types.HiddenUserServer && a.client != nil && a.client.Store != nil && a.client.Store.LIDs != nil {
		if pn, err := a.client.Store.LIDs.GetPNForLID(ctx, chat.ToNonAD()); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD().String()
		}
	}
	return chat.ToNonAD().String()
}

// handleMessage stores a live message event.
func (a *App) handleMessage(evt *events.Message) {
	ctx := context.Background()
	info := evt.Info
	chatJID := a.chatJIDString(ctx, info.Chat)
	sender := a.senderID(ctx, info)
	msg := evt.Message

	if info.PushName != "" && !info.IsFromMe {
		a.store.UpsertContact(sender+"@"+types.DefaultUserServer, "", info.PushName)
	}

	// Protocol messages: edits and deletions for everyone.
	if pm := msg.GetProtocolMessage(); pm != nil {
		target := pm.GetKey().GetID()
		switch pm.GetType() {
		case waProto.ProtocolMessage_REVOKE:
			if target != "" {
				a.store.MarkMessageDeleted(chatJID, target)
			}
		case waProto.ProtocolMessage_MESSAGE_EDIT:
			if target != "" {
				if text := extractTextContent(pm.GetEditedMessage()); text != "" {
					a.store.UpdateMessageContent(chatJID, target, text)
				}
			}
		}
		return
	}
	if evt.IsEdit {
		if text := extractTextContent(msg); text != "" {
			a.store.UpdateMessageContent(chatJID, info.ID, text)
		}
		return
	}

	if r := msg.GetReactionMessage(); r != nil {
		a.store.StoreReaction(chatJID, r.GetKey().GetID(), sender, r.GetText(), info.Timestamp)
		return
	}

	content := extractTextContent(msg)
	media := extractMediaInfo(msg, info.ID)
	if content == "" && media.Type == "" {
		return
	}

	name := a.chatName(ctx, info.Chat, chatJID, nil, sender)
	if err := a.store.StoreChat(chatJID, name, info.Timestamp); err != nil {
		a.log.Warnf("Failed to store chat: %v", err)
	}
	senderName := ""
	if !info.IsFromMe {
		senderName = info.PushName
	}
	err := a.store.StoreMessage(StoredMessage{
		ID: info.ID, ChatJID: chatJID, Sender: sender, SenderName: senderName, Content: content,
		Timestamp: info.Timestamp, IsFromMe: info.IsFromMe,
		MediaType: media.Type, Filename: media.Filename, URL: media.URL, MediaKey: media.MediaKey,
		FileSHA256: media.FileSHA256, FileEncSHA256: media.FileEncSHA256, FileLength: media.FileLength,
		QuotedID: quotedID(msg),
	})
	if err != nil {
		a.log.Warnf("Failed to store message: %v", err)
		return
	}
	a.state.noteMessage()
	a.logMessage(info.Timestamp, info.IsFromMe, name, sender, media.Type, content)
}

// chatName finds a human readable name for a chat.
func (a *App) chatName(ctx context.Context, jid types.JID, chatJID string, conv *waHistorySync.Conversation, sender string) string {
	if conv != nil {
		if n := conv.GetDisplayName(); n != "" {
			return n
		}
		if n := conv.GetName(); n != "" {
			return n
		}
	}
	if existing := a.store.ChatName(chatJID); existing != "" {
		return existing
	}
	if jid.Server == types.GroupServer {
		if a.client != nil {
			if gi, err := a.client.GetGroupInfo(ctx, jid); err == nil && gi.Name != "" {
				return gi.Name
			}
		}
		return "Group " + jid.User
	}
	if a.client != nil && a.client.Store != nil && a.client.Store.Contacts != nil {
		lookup := jid.ToNonAD()
		if parsed, err := types.ParseJID(chatJID); err == nil {
			lookup = parsed
		}
		if c, err := a.client.Store.Contacts.GetContact(ctx, lookup); err == nil {
			for _, n := range []string{c.FullName, c.FirstName, c.BusinessName, c.PushName} {
				if n != "" {
					return n
				}
			}
		}
	}
	if sender != "" {
		return sender
	}
	return jid.User
}

// handleHistorySync stores the history WhatsApp sends after pairing.
func (a *App) handleHistorySync(evt *events.HistorySync) {
	ctx := context.Background()
	convs := evt.Data.GetConversations()
	synced := 0
	for _, conv := range convs {
		rawJID := conv.GetID()
		if rawJID == "" {
			continue
		}
		jid, err := types.ParseJID(rawJID)
		if err != nil {
			a.log.Warnf("Failed to parse JID %s: %v", rawJID, err)
			continue
		}
		chatJID := a.chatJIDString(ctx, jid)
		name := a.chatName(ctx, jid, chatJID, conv, "")

		var newest time.Time
		for _, hm := range conv.GetMessages() {
			wm := hm.GetMessage()
			if wm == nil || wm.GetMessage() == nil {
				continue
			}
			ts := wm.GetMessageTimestamp()
			if ts == 0 {
				continue
			}
			t := time.Unix(int64(ts), 0)
			key := wm.GetKey()
			id := key.GetID()
			isFromMe := key.GetFromMe()
			sender := jid.User
			if isFromMe && a.client != nil && a.client.Store.ID != nil {
				sender = a.client.Store.ID.User
			} else if p := key.GetParticipant(); p != "" {
				sender = strings.SplitN(p, "@", 2)[0]
				if pj, err := types.ParseJID(p); err == nil && pj.Server == types.HiddenUserServer && a.client != nil {
					if pn, err := a.client.Store.LIDs.GetPNForLID(ctx, pj.ToNonAD()); err == nil && !pn.IsEmpty() {
						sender = pn.User
					}
				}
			} else if wm.GetParticipant() != "" {
				sender = strings.SplitN(wm.GetParticipant(), "@", 2)[0]
			}

			if r := wm.GetMessage().GetReactionMessage(); r != nil {
				a.store.StoreReaction(chatJID, r.GetKey().GetID(), sender, r.GetText(), t)
				continue
			}
			content := extractTextContent(wm.GetMessage())
			media := extractMediaInfo(wm.GetMessage(), id)
			if content == "" && media.Type == "" {
				continue
			}
			if t.After(newest) {
				newest = t
			}
			pushName := ""
			if !isFromMe {
				pushName = wm.GetPushName()
			}
			err := a.store.StoreMessage(StoredMessage{
				ID: id, ChatJID: chatJID, Sender: sender, SenderName: pushName, Content: content, Timestamp: t, IsFromMe: isFromMe,
				MediaType: media.Type, Filename: media.Filename, URL: media.URL, MediaKey: media.MediaKey,
				FileSHA256: media.FileSHA256, FileEncSHA256: media.FileEncSHA256, FileLength: media.FileLength,
				QuotedID: quotedID(wm.GetMessage()),
			})
			if err != nil {
				// The chat row may not exist yet (foreign key); create it and retry once.
				a.store.StoreChat(chatJID, name, t)
				err = a.store.StoreMessage(StoredMessage{
					ID: id, ChatJID: chatJID, Sender: sender, SenderName: pushName, Content: content, Timestamp: t, IsFromMe: isFromMe,
					MediaType: media.Type, Filename: media.Filename, URL: media.URL, MediaKey: media.MediaKey,
					FileSHA256: media.FileSHA256, FileEncSHA256: media.FileEncSHA256, FileLength: media.FileLength,
					QuotedID: quotedID(wm.GetMessage()),
				})
			}
			if err != nil {
				a.log.Warnf("Failed to store history message: %v", err)
				continue
			}
			synced++
		}
		if !newest.IsZero() {
			a.store.StoreChat(chatJID, name, newest)
		}
	}
	a.state.noteHistory(synced)
	fmt.Printf("History sync: %d conversations, %d messages stored\n", len(convs), synced)
}

// handlePushNames stores display names WhatsApp sends in bulk.
func (a *App) handlePushName(evt *events.PushName) {
	if evt.JID.Server == types.DefaultUserServer && evt.NewPushName != "" {
		a.store.UpsertContact(evt.JID.ToNonAD().String(), "", evt.NewPushName)
	}
}

// handleGroupInfo keeps group names up to date.
func (a *App) handleGroupInfo(evt *events.GroupInfo) {
	if evt.Name != nil && evt.Name.Name != "" {
		a.store.RenameChat(evt.JID.ToNonAD().String(), evt.Name.Name)
	}
}

// handleContact stores names from the address book sync.
func (a *App) handleContact(evt *events.Contact) {
	if evt.Action == nil {
		return
	}
	name := evt.Action.GetFullName()
	if name == "" {
		name = evt.Action.GetFirstName()
	}
	if name == "" {
		return
	}
	jid := evt.JID.ToNonAD().String()
	a.store.UpsertContact(jid, name, "")
	a.store.RenameChat(jid, name)
}

// requestHistorySync asks the phone for older messages of one chat.
func (a *App) requestHistorySync(ctx context.Context, chat types.JID, count int) error {
	if !a.client.IsConnected() || a.client.Store.ID == nil {
		return fmt.Errorf("not connected")
	}
	// Use the oldest stored message of the chat as anchor.
	var oldestID string
	var oldest time.Time
	var fromMe bool
	err := a.store.db.QueryRow("SELECT id, timestamp, is_from_me FROM messages WHERE chat_jid = ? ORDER BY timestamp ASC LIMIT 1", chat.String()).Scan(&oldestID, &oldest, &fromMe)
	if err != nil {
		return fmt.Errorf("no stored message in this chat to anchor the history request: %w", err)
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: fromMe},
		ID:            oldestID,
		Timestamp:     oldest,
	}
	req := a.client.BuildHistorySyncRequest(info, count)
	_, err = a.client.SendMessage(ctx, a.client.Store.ID.ToNonAD(), req, whatsmeow.SendRequestExtra{Peer: true})
	return err
}
