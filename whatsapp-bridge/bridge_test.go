package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"loopback default", Config{Addr: "127.0.0.1:8080", LogLevel: "INFO"}, false},
		{"localhost", Config{Addr: "localhost:8080", LogLevel: "INFO"}, false},
		{"ipv6 loopback", Config{Addr: "[::1]:8080", LogLevel: "INFO"}, false},
		{"lan without token", Config{Addr: "0.0.0.0:8080", LogLevel: "INFO"}, true},
		{"lan with token", Config{Addr: "0.0.0.0:8080", Token: "s3cret", LogLevel: "INFO"}, false},
		{"lan insecure opt-in", Config{Addr: "0.0.0.0:8080", AllowInsecure: true, LogLevel: "INFO"}, false},
		{"bad addr", Config{Addr: "8080", LogLevel: "INFO"}, true},
		{"bad level", Config{Addr: "127.0.0.1:8080", LogLevel: "LOUD"}, true},
	}
	for _, c := range cases {
		if err := c.cfg.Validate(); (err != nil) != c.wantErr {
			t.Errorf("%s: Validate() error = %v, wantErr %v", c.name, err, c.wantErr)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("WHATSAPP_BRIDGE_ADDR", "")
	t.Setenv("WHATSAPP_BRIDGE_PORT", "9090")
	cfg := LoadConfig()
	if cfg.Addr != "127.0.0.1:9090" {
		t.Fatalf("Addr = %q, want 127.0.0.1:9090", cfg.Addr)
	}
	if cfg.LogContent {
		t.Fatal("message content must not be logged by default")
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"report.pdf":             "report.pdf",
		"../../etc/passwd":       "passwd",
		`..\..\Windows\evil.exe`: "evil.exe",
		"a:b*c?.txt":             "a_b_c_.txt",
		"  spaced name .docx ":   "spaced_name_.docx",
		"..":                     "",
		"":                       "",
		"bad\x00name.png":        "badname.png",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("x", 300) + ".pdf"
	if got := safeFilename(long); len(got) > 150 || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("long name not shortened correctly: %d %q", len(got), got[len(got)-8:])
	}
}

func TestParseRecipient(t *testing.T) {
	ok := map[string]string{
		"41781234567":                "41781234567@s.whatsapp.net",
		"+41 78 123 45 67":           "41781234567@s.whatsapp.net",
		"0041781234567":              "41781234567@s.whatsapp.net",
		"(415) 555-0100 1":           "41555501001@s.whatsapp.net",
		"123456789@g.us":             "123456789@g.us",
		"41781234567@s.whatsapp.net": "41781234567@s.whatsapp.net",
	}
	for in, want := range ok {
		jid, err := parseRecipient(in)
		if err != nil || jid.String() != want {
			t.Errorf("parseRecipient(%q) = %v, %v; want %s", in, jid, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "12", "4178123456712345678", "41;DROP"} {
		if _, err := parseRecipient(bad); err == nil {
			t.Errorf("parseRecipient(%q) should fail", bad)
		}
	}
}

func TestDetectMedia(t *testing.T) {
	cases := []struct {
		path string
		typ  whatsmeow.MediaType
		mime string
	}{
		{"photo.JPG", whatsmeow.MediaImage, "image/jpeg"},
		{"clip.mp4", whatsmeow.MediaVideo, "video/mp4"},
		{"voice.ogg", whatsmeow.MediaAudio, "audio/ogg; codecs=opus"},
		{"doc.pdf", whatsmeow.MediaDocument, "application/pdf"},
	}
	for _, c := range cases {
		typ, mt := detectMedia(c.path, nil)
		if typ != c.typ || mt != c.mime {
			t.Errorf("detectMedia(%q) = %s %s, want %s %s", c.path, typ, mt, c.typ, c.mime)
		}
	}
	if typ, mt := detectMedia("noext", []byte("%PDF-1.7 hello")); typ != whatsmeow.MediaDocument || mt != "application/pdf" {
		t.Errorf("content sniffing failed: %s %s", typ, mt)
	}
}

func TestExtractTextContent(t *testing.T) {
	cases := []struct {
		name string
		msg  *waProto.Message
		want string
	}{
		{"plain", &waProto.Message{Conversation: proto.String("hi")}, "hi"},
		{"extended", &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("link")}}, "link"},
		{"caption", &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("look")}}, "look"},
		{"ephemeral", &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{Conversation: proto.String("secret")}}}, "secret"},
		{"poll", &waProto.Message{PollCreationMessage: &waProto.PollCreationMessage{Name: proto.String("Pizza?"),
			Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("Yes")}, {OptionName: proto.String("No")}}}}, "[poll] Pizza? (Yes / No)"},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got := extractTextContent(c.msg); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestQuotedIDAndMedia(t *testing.T) {
	msg := &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
		Text: proto.String("reply"), ContextInfo: &waProto.ContextInfo{StanzaID: proto.String("ABC123")}}}
	if got := quotedID(msg); got != "ABC123" {
		t.Fatalf("quotedID = %q", got)
	}
	doc := &waProto.Message{DocumentMessage: &waProto.DocumentMessage{FileName: proto.String("../../x.pdf"), Mimetype: proto.String("application/pdf")}}
	mi := extractMediaInfo(doc, "MSGID")
	if mi.Type != "document" || mi.Filename != "MSGID_x.pdf" {
		t.Fatalf("unexpected media info: %+v", mi)
	}
	img := extractMediaInfo(&waProto.Message{ImageMessage: &waProto.ImageMessage{Mimetype: proto.String("image/png")}}, "ID9")
	if img.Filename != "image_ID9.png" {
		t.Fatalf("image filename = %q", img.Filename)
	}
}

// buildOgg creates a minimal two-page Ogg Opus stream.
func buildOgg(granule uint64, preSkip uint16) []byte {
	page := func(seq uint32, gran uint64, payload []byte) []byte {
		var b bytes.Buffer
		b.WriteString("OggS")
		b.WriteByte(0)
		b.WriteByte(0)
		binary.Write(&b, binary.LittleEndian, gran)
		binary.Write(&b, binary.LittleEndian, uint32(1))
		binary.Write(&b, binary.LittleEndian, seq)
		binary.Write(&b, binary.LittleEndian, uint32(0))
		b.WriteByte(1)
		b.WriteByte(byte(len(payload)))
		b.Write(payload)
		return b.Bytes()
	}
	head := []byte("OpusHead")
	head = append(head, 1, 1)
	head = binary.LittleEndian.AppendUint16(head, preSkip)
	head = binary.LittleEndian.AppendUint32(head, 16000)
	head = append(head, 0, 0, 0)
	out := page(0, 0, head)
	return append(out, page(1, granule, []byte("audio"))...)
}

func TestAnalyzeOggOpus(t *testing.T) {
	// 5 seconds at the fixed 48 kHz Opus clock, even though the input rate says 16 kHz.
	d, wf, err := analyzeOggOpus(buildOgg(48000*5+312, 312))
	if err != nil {
		t.Fatal(err)
	}
	if d != 5 {
		t.Fatalf("duration = %d, want 5", d)
	}
	if len(wf) != 64 {
		t.Fatalf("waveform length = %d", len(wf))
	}
	for _, v := range wf {
		if v > 100 {
			t.Fatalf("waveform value out of range: %d", v)
		}
	}
	if _, _, err := analyzeOggOpus([]byte("RIFF....")); err == nil {
		t.Fatal("expected error for non-ogg data")
	}
	// Truncated pages must not panic.
	analyzeOggOpus(buildOgg(48000, 0)[:40])
}

func TestQRSVG(t *testing.T) {
	svg, err := qrSVG("2@abc,def,ghi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "<path") {
		t.Fatalf("unexpected svg: %.80s", svg)
	}
}

func newTestStore(t *testing.T) *MessageStore {
	t.Helper()
	s, err := NewMessageStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigratesOldDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Schema of the original project.
	_, err = db.Exec(`CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, is_from_me BOOLEAN,
		media_type TEXT, filename TEXT, url TEXT, media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB, file_length INTEGER,
		PRIMARY KEY (id, chat_jid), FOREIGN KEY (chat_jid) REFERENCES chats(jid));
		INSERT INTO chats VALUES ('1@s.whatsapp.net', 'Old', '2025-01-01 10:00:00+01:00');
		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES ('m1', '1@s.whatsapp.net', '1', 'hello', '2025-01-01 10:00:00+01:00', 0);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := NewMessageStore(dir)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	defer s.Close()
	m, err := s.GetMessage("1@s.whatsapp.net", "m1")
	if err != nil || m.Content != "hello" {
		t.Fatalf("old message lost after migration: %+v %v", m, err)
	}
	// Running the migration twice must be harmless.
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreChatNeverGoesBack(t *testing.T) {
	s := newTestStore(t)
	newer := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-48 * time.Hour)
	if err := s.StoreChat("a@s.whatsapp.net", "Alice", newer); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreChat("a@s.whatsapp.net", "", older); err != nil {
		t.Fatal(err)
	}
	var name string
	var ts time.Time
	s.db.QueryRow("SELECT name, last_message_time FROM chats WHERE jid = 'a@s.whatsapp.net'").Scan(&name, &ts)
	if name != "Alice" || !ts.Equal(newer) {
		t.Fatalf("got %q %v, want Alice %v", name, ts, newer)
	}
}

func TestReactionsAndContacts(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.StoreReaction("c", "m", "u", "👍", now)
	s.StoreReaction("c", "m", "u", "❤️", now)
	var n int
	var emoji string
	s.db.QueryRow("SELECT COUNT(*), MAX(emoji) FROM reactions").Scan(&n, &emoji)
	if n != 1 || emoji != "❤️" {
		t.Fatalf("reaction not replaced: %d %q", n, emoji)
	}
	s.StoreReaction("c", "m", "u", "", now)
	s.db.QueryRow("SELECT COUNT(*) FROM reactions").Scan(&n)
	if n != 0 {
		t.Fatal("empty emoji must remove the reaction")
	}
	s.UpsertContact("41@s.whatsapp.net", "", "Pushy")
	s.UpsertContact("41@s.whatsapp.net", "Real Name", "")
	var name, push string
	s.db.QueryRow("SELECT name, push_name FROM contacts").Scan(&name, &push)
	if name != "Real Name" || push != "Pushy" {
		t.Fatalf("contact merge wrong: %q %q", name, push)
	}
}

func TestScheduledLifecycle(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	id1, _ := s.AddScheduled(ScheduledMessage{Recipient: "41781234567", Message: "due", SendAt: now.Add(-time.Minute)})
	id2, _ := s.AddScheduled(ScheduledMessage{Recipient: "41781234567", Message: "later", SendAt: now.Add(time.Hour)})
	due, err := s.DueScheduled(now)
	if err != nil || len(due) != 1 || due[0].ID != id1 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	s.FinishScheduled(id1, nil)
	if ok, _ := s.CancelScheduled(id1); ok {
		t.Fatal("a sent message must not be cancellable")
	}
	if ok, _ := s.CancelScheduled(id2); !ok {
		t.Fatal("pending message should be cancellable")
	}
	all, _ := s.ListScheduled("")
	if len(all) != 2 || all[0].Status != "sent" || all[1].Status != "cancelled" || all[0].SentAt == nil {
		t.Fatalf("unexpected list: %+v", all)
	}
}

func newTestApp(t *testing.T, cfg Config) *App {
	t.Helper()
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8080"
	}
	cfg.StoreDir = t.TempDir()
	s, err := NewMessageStore(cfg.StoreDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &App{cfg: cfg, store: s, state: NewBridgeState(), log: nopLogger{}}
}

type nopLogger struct{}

func (nopLogger) Warnf(string, ...interface{})  {}
func (nopLogger) Errorf(string, ...interface{}) {}
func (nopLogger) Infof(string, ...interface{})  {}
func (nopLogger) Debugf(string, ...interface{}) {}
func (n nopLogger) Sub(string) waLog.Logger     { return n }

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8080"
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPISecurity(t *testing.T) {
	app := newTestApp(t, Config{})
	h := app.routes()
	jsonHdr := map[string]string{"Content-Type": "application/json"}

	if r := do(h, "GET", "/api/health", "", nil); r.Code != 200 {
		t.Fatalf("health: %d", r.Code)
	}
	if r := do(h, "GET", "/api/status", "", map[string]string{"Host": "attacker.example:8080"}); r.Code != 403 {
		t.Fatalf("DNS rebinding host must be rejected, got %d", r.Code)
	}
	if r := do(h, "POST", "/api/send", `{"recipient":"41781234567","message":"x"}`, map[string]string{"Content-Type": "text/plain"}); r.Code != 415 {
		t.Fatalf("text/plain POST (CSRF via form) must be rejected, got %d", r.Code)
	}
	if r := do(h, "POST", "/api/send", `{}`, map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}); r.Code != 403 {
		t.Fatalf("cross-origin POST must be rejected, got %d", r.Code)
	}
	if r := do(h, "POST", "/api/send", `{"recipient":"41781234567","message":"x"}`, jsonHdr); r.Code != 503 {
		t.Fatalf("send without connection should be 503, got %d: %s", r.Code, r.Body)
	}
	if r := do(h, "POST", "/api/send", `{"message":"x"}`, jsonHdr); r.Code != 400 {
		t.Fatalf("missing recipient should be 400, got %d", r.Code)
	}
	if r := do(h, "GET", "/", "", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "WhatsApp Bridge") {
		t.Fatalf("dashboard: %d", r.Code)
	}
}

func TestAPIToken(t *testing.T) {
	app := newTestApp(t, Config{Addr: "0.0.0.0:8080", Token: "tok"})
	h := app.routes()
	if r := do(h, "GET", "/api/status", "", map[string]string{"Host": "192.168.1.5:8080"}); r.Code != 401 {
		t.Fatalf("missing token should be 401, got %d", r.Code)
	}
	if r := do(h, "GET", "/api/status", "", map[string]string{"Host": "192.168.1.5:8080", "Authorization": "Bearer wrong"}); r.Code != 401 {
		t.Fatalf("wrong token should be 401, got %d", r.Code)
	}
	r := do(h, "GET", "/api/status", "", map[string]string{"Host": "192.168.1.5:8080", "Authorization": "Bearer tok"})
	if r.Code != 200 {
		t.Fatalf("valid token should be 200, got %d", r.Code)
	}
	var body map[string]any
	json.Unmarshal(r.Body.Bytes(), &body)
	if body["token_required"] != true {
		t.Fatalf("status should report token protection: %v", body)
	}
	// Token login through the dashboard sets an HttpOnly cookie.
	r = do(h, "GET", "/?token=tok", "", map[string]string{"Host": "192.168.1.5:8080"})
	if r.Code != http.StatusSeeOther || !strings.Contains(r.Header().Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("dashboard token login failed: %d %s", r.Code, r.Header().Get("Set-Cookie"))
	}
}

func TestAPISchedule(t *testing.T) {
	app := newTestApp(t, Config{})
	h := app.routes()
	jsonHdr := map[string]string{"Content-Type": "application/json"}
	future := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	r := do(h, "POST", "/api/schedule", `{"recipient":"41781234567","message":"hi","send_at":"`+future+`"}`, jsonHdr)
	if r.Code != 200 {
		t.Fatalf("schedule: %d %s", r.Code, r.Body)
	}
	if r := do(h, "POST", "/api/schedule", `{"recipient":"41781234567","message":"hi","send_at":"tomorrow"}`, jsonHdr); r.Code != 400 {
		t.Fatalf("bad time should be 400, got %d", r.Code)
	}
	if r := do(h, "POST", "/api/schedule", `{"recipient":"41781234567","message":"hi","send_at":"2001-01-01T00:00:00Z"}`, jsonHdr); r.Code != 400 {
		t.Fatalf("past time should be 400, got %d", r.Code)
	}
	if r := do(h, "GET", "/api/schedule?status=pending", "", nil); !strings.Contains(r.Body.String(), `"message":"hi"`) {
		t.Fatalf("list: %s", r.Body)
	}
	if r := do(h, "DELETE", "/api/schedule/1", "", nil); r.Code != 200 {
		t.Fatalf("cancel: %d %s", r.Code, r.Body)
	}
	if r := do(h, "DELETE", "/api/schedule/1", "", nil); r.Code != 404 {
		t.Fatalf("second cancel should be 404, got %d", r.Code)
	}
}

func TestSchedulerSkipsWhenOffline(t *testing.T) {
	app := newTestApp(t, Config{})
	app.store.AddScheduled(ScheduledMessage{Recipient: "41781234567", Message: "x", SendAt: time.Now().Add(-time.Minute)})
	if n := app.sendDueScheduled(t.Context(), time.Now()); n != 0 {
		t.Fatalf("scheduler must not send while disconnected, attempted %d", n)
	}
	pending, _ := app.store.ListScheduled("pending")
	if len(pending) != 1 {
		t.Fatal("message should stay pending until the bridge is connected")
	}
}

func TestIsLocalHostHeader(t *testing.T) {
	for _, h := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "localhost"} {
		if !isLocalHostHeader(h) {
			t.Errorf("%s should be local", h)
		}
	}
	for _, h := range []string{"evil.com", "192.168.0.10:8080", "127.0.0.1.evil.com"} {
		if isLocalHostHeader(h) {
			t.Errorf("%s must not be local", h)
		}
	}
}
