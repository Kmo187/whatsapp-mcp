package main

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"go.mau.fi/whatsmeow"
)

// safeFilename strips directories and characters that are unsafe on Windows,
// macOS or Linux. It prevents a received document called "../../evil.exe"
// from being written outside the media folder.
func safeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSpace(filepath.Base("/" + name))
	if name == "/" || name == "." || name == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || r == 127:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		case unicode.IsSpace(r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ". ")
	if len(out) > 150 {
		ext := filepath.Ext(out)
		if len(ext) > 10 {
			ext = ""
		}
		out = out[:150-len(ext)] + ext
	}
	return out
}

// safeChatDir turns a chat JID into a folder name.
func safeChatDir(chatJID string) string {
	return safeFilename(strings.NewReplacer(":", "_", "@", "_at_").Replace(chatJID))
}

// extForMime returns a file extension for a MIME type, or def.
func extForMime(mimeType, def string) string {
	mt := strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	switch mt {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "application/pdf":
		return ".pdf"
	}
	if mt != "" {
		if exts, err := mime.ExtensionsByType(mt); err == nil && len(exts) > 0 {
			return exts[0]
		}
	}
	return def
}

// detectMedia decides how a local file is sent: as image, video, voice note or document.
func detectMedia(path string, data []byte) (whatsmeow.MediaType, string) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		return whatsmeow.MediaImage, "image/jpeg"
	case ".png":
		return whatsmeow.MediaImage, "image/png"
	case ".webp":
		return whatsmeow.MediaImage, "image/webp"
	case ".gif":
		return whatsmeow.MediaImage, "image/gif"
	case ".ogg", ".opus":
		return whatsmeow.MediaAudio, "audio/ogg; codecs=opus"
	case ".mp4", ".m4v":
		return whatsmeow.MediaVideo, "video/mp4"
	case ".mov":
		return whatsmeow.MediaVideo, "video/quicktime"
	case ".3gp":
		return whatsmeow.MediaVideo, "video/3gpp"
	}
	mt := ""
	if ext != "" {
		mt = mime.TypeByExtension(ext)
	}
	if mt == "" && len(data) > 0 {
		mt = http.DetectContentType(data)
	}
	if mt == "" {
		mt = "application/octet-stream"
	}
	return whatsmeow.MediaDocument, mt
}

// MediaDownloader implements whatsmeow.DownloadableMessage for stored media info.
type MediaDownloader struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

func (d *MediaDownloader) GetDirectPath() string             { return d.DirectPath }
func (d *MediaDownloader) GetURL() string                    { return d.URL }
func (d *MediaDownloader) GetMediaKey() []byte               { return d.MediaKey }
func (d *MediaDownloader) GetFileLength() uint64             { return d.FileLength }
func (d *MediaDownloader) GetFileSHA256() []byte             { return d.FileSHA256 }
func (d *MediaDownloader) GetFileEncSHA256() []byte          { return d.FileEncSHA256 }
func (d *MediaDownloader) GetMediaType() whatsmeow.MediaType { return d.MediaType }

// extractDirectPathFromURL converts a media URL into the direct path WhatsApp expects.
func extractDirectPathFromURL(url string) string {
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url
	}
	return "/" + strings.SplitN(parts[1], "?", 2)[0]
}

func waMediaType(t string) (whatsmeow.MediaType, error) {
	switch t {
	case "image", "sticker":
		return whatsmeow.MediaImage, nil
	case "video":
		return whatsmeow.MediaVideo, nil
	case "audio":
		return whatsmeow.MediaAudio, nil
	case "document":
		return whatsmeow.MediaDocument, nil
	}
	return "", fmt.Errorf("unsupported media type: %s", t)
}

// DownloadResult describes a downloaded attachment.
type DownloadResult struct {
	MediaType string
	Filename  string
	Path      string
	Cached    bool
}

// downloadMedia fetches a stored attachment and saves it under <store>/media/<chat>/.
func (a *App) downloadMedia(ctx context.Context, messageID, chatJID string) (DownloadResult, error) {
	m, err := a.store.GetMessage(chatJID, messageID)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("message not found: %w", err)
	}
	if m.MediaType == "" {
		return DownloadResult{}, fmt.Errorf("not a media message")
	}
	filename := safeFilename(m.Filename)
	if filename == "" {
		filename = m.MediaType + "_" + safeFilename(messageID)
	}
	dir := filepath.Join(a.cfg.StoreDir, "media", safeChatDir(chatJID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return DownloadResult{}, fmt.Errorf("failed to create media directory: %w", err)
	}
	localPath := filepath.Join(dir, filename)
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return DownloadResult{}, err
	}
	if st, err := os.Stat(localPath); err == nil && st.Size() > 0 {
		return DownloadResult{m.MediaType, filename, absPath, true}, nil
	}
	// Older versions saved files in store/<chat>/; reuse them if present.
	legacy := filepath.Join(a.cfg.StoreDir, strings.ReplaceAll(chatJID, ":", "_"), m.Filename)
	if st, err := os.Stat(legacy); err == nil && st.Size() > 0 && safeFilename(m.Filename) == m.Filename {
		if abs, err := filepath.Abs(legacy); err == nil {
			return DownloadResult{m.MediaType, m.Filename, abs, true}, nil
		}
	}
	if m.URL == "" || len(m.MediaKey) == 0 || len(m.FileSHA256) == 0 || len(m.FileEncSHA256) == 0 || m.FileLength == 0 {
		return DownloadResult{}, fmt.Errorf("incomplete media information for download")
	}
	if !a.client.IsConnected() {
		return DownloadResult{}, fmt.Errorf("not connected to WhatsApp")
	}
	mt, err := waMediaType(m.MediaType)
	if err != nil {
		return DownloadResult{}, err
	}
	data, err := a.client.Download(ctx, &MediaDownloader{
		URL: m.URL, DirectPath: extractDirectPathFromURL(m.URL), MediaKey: m.MediaKey, FileLength: m.FileLength,
		FileSHA256: m.FileSHA256, FileEncSHA256: m.FileEncSHA256, MediaType: mt,
	})
	if err != nil {
		return DownloadResult{}, fmt.Errorf("failed to download media: %w", err)
	}
	if err := os.WriteFile(localPath, data, 0o600); err != nil {
		return DownloadResult{}, fmt.Errorf("failed to save media file: %w", err)
	}
	return DownloadResult{m.MediaType, filename, absPath, false}, nil
}
