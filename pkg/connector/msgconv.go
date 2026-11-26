package connector

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

func messageMetaFromLib(msg libtiktok.Message) *MessageMetadata {
	return &MessageMetadata{
		MsgType:      msg.Type,
		SendChainID:  msg.SendChainID,
		SenderSecUID: msg.SenderSecUID,
		CursorTsUs:   msg.CursorTsUs,
		ContentJSON:  string(msg.RawContentJSON),
	}
}

// convertMessage converts a libtiktok.Message into a bridgev2.ConvertedMessage
// that the central bridge module will forward to the Matrix room.
//
// This function is used as the ConvertMessageFunc on simplevent.Message[libtiktok.Message].
func (tc *TikTokClient) convertMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	// TikTok sometimes emits an extra row with only placeholder content_json (e.g. {"hack":"1"})
	// and no usable attachment — nothing to bridge without duplicating the real media message.
	if ignorableTikTokCompanionMessage(msg) {
		return nil, bridgev2.ErrIgnoringRemoteEvent
	}
	switch msg.Type {
	case "text":
		return tc.convertTextMessage(ctx, portal, msg), nil
	case "image":
		return tc.convertImageMessage(ctx, portal, intent, msg)
	case "sticker":
		return tc.convertStickerMessage(ctx, portal, intent, msg)
	case "video":
		return tc.convertVideoMessage(ctx, portal, intent, msg)
	case "shared_post":
		return tc.convertSharedPostMessage(ctx, portal, intent, msg)
	default:
		// Any other unsupported type falls back to a
		// notice so the user knows something arrived even if we can't render it.
		return convertUnknownMessage(msg), nil
	}
}

// ignorableTikTokCompanionMessage reports rows that have no displayable payload after parsing.
// They are not errors — often a duplicate wire envelope next to a full private_image/video row.
func ignorableTikTokCompanionMessage(msg libtiktok.Message) bool {
	if strings.TrimSpace(msg.Text) != "" {
		return false
	}
	if msg.MediaURL != "" || msg.ThumbnailURL != "" || msg.MediaDecryptKey != "" {
		return false
	}
	if msg.ReplyToServerID != 0 || len(msg.Reactions) > 0 {
		return false
	}
	if msg.Type != "" {
		return false
	}
	// Failed private media / sticker parses still deserve a notice, not silent drop.
	switch strings.ToLower(msg.MessageSubtype) {
	case "private_image", "private_video", "sticker":
		return false
	default:
		return true
	}
}

// convertImageMessage downloads a TikTok DM private image, decrypts it via the
// libtiktok client, and uploads it to Matrix as an m.image event.
func (tc *TikTokClient) convertImageMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	log := zerolog.Ctx(ctx)

	if msg.MediaURL == "" || msg.MediaDecryptKey == "" {
		return convertImageFallback(msg), nil
	}

	data, mime, err := tc.apiClient.DownloadPrivateImage(ctx, msg.MediaURL, msg.MediaDecryptKey)
	if err != nil && msg.ThumbnailURL != "" && msg.ThumbnailURL != msg.MediaURL {
		log.Warn().Err(err).
			Str("url", msg.MediaURL).
			Str("thumbnail_url", msg.ThumbnailURL).
			Msg("Failed to download full TikTok private image; trying thumbnail")
		data, mime, err = tc.apiClient.DownloadPrivateImage(ctx, msg.ThumbnailURL, msg.MediaDecryptKey)
	}
	if err != nil {
		log.Warn().Err(err).
			Str("url", msg.MediaURL).
			Str("thumbnail_url", msg.ThumbnailURL).
			Msg("Failed to download TikTok private image; falling back to text")
		return convertImageFallback(msg), nil
	}

	fileName := imageFileName(mime)
	size := int64(len(data))
	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     int(size),
		},
	}
	if msg.MediaWidth > 0 {
		content.Info.Width = msg.MediaWidth
	}
	if msg.MediaHeight > 0 {
		content.Info.Height = msg.MediaHeight
	}

	mxcURL, encFile, err := intent.UploadMediaStream(
		ctx,
		portal.MXID,
		size,
		false,
		func(dest io.Writer) (*bridgev2.FileStreamResult, error) {
			if _, err := dest.Write(data); err != nil {
				return nil, err
			}
			return &bridgev2.FileStreamResult{
				FileName: fileName,
				MimeType: mime,
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("upload image to Matrix: %w", err)
	}

	content.URL = mxcURL
	content.File = encFile

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: messageMetaFromLib(msg),
			},
		},
	}, nil
}

// convertStickerMessage downloads a TikTok DM sticker from the signed CDN URL
// carried in the message payload and uploads it to Matrix as an m.image event.
// If the sticker cannot be fetched, it falls back to a small text notice.
func (tc *TikTokClient) convertStickerMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	log := zerolog.Ctx(ctx)

	if msg.MediaURL == "" {
		return convertStickerFallback(msg), nil
	}

	data, mime, err := tc.apiClient.DownloadSticker(ctx, msg.MediaURL)
	if err != nil {
		log.Warn().Err(err).Str("url", msg.MediaURL).
			Msg("Failed to download TikTok sticker; falling back to text")
		return convertStickerFallback(msg), nil
	}

	fileName := stickerFileName(mime)
	size := int64(len(data))
	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     int(size),
		},
	}

	mxcURL, encFile, err := intent.UploadMediaStream(
		ctx,
		portal.MXID,
		size,
		false,
		func(dest io.Writer) (*bridgev2.FileStreamResult, error) {
			if _, err := dest.Write(data); err != nil {
				return nil, err
			}
			return &bridgev2.FileStreamResult{
				FileName: fileName,
				MimeType: mime,
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("upload sticker to Matrix: %w", err)
	}

	content.URL = mxcURL
	content.File = encFile

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: messageMetaFromLib(msg),
			},
		},
	}, nil
}

// convertTextMessage converts a plain-text TikTok DM. For replies (ReplyToServerID), it sets
// ConvertedMessage.ReplyTo so bridgev2 can attach m.relates_to, and when the parent message
// is already in the bridge DB it adds Matrix rich-reply fallbacks (plain + mx-reply HTML)
// so clients render the quote UI reliably.
func (*TikTokClient) convertTextMessage(ctx context.Context, portal *bridgev2.Portal, msg libtiktok.Message) *bridgev2.ConvertedMessage {
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    msg.Text,
	}
	meta := messageMetaFromLib(msg)
	cm := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{Type: event.EventMessage, Content: content, DBMetadata: meta},
		},
	}
	if msg.ReplyToServerID == 0 {
		return cm
	}
	parentRemoteID := networkid.MessageID(strconv.FormatUint(msg.ReplyToServerID, 10))
	cm.ReplyTo = &networkid.MessageOptionalPartID{MessageID: parentRemoteID}

	if portal == nil {
		return cm
	}
	parent, err := portal.Bridge.DB.Message.GetFirstPartByID(ctx, portal.Receiver, parentRemoteID)
	if err != nil || parent == nil || parent.MXID == "" {
		return cm
	}

	quoted := strings.TrimSpace(msg.ReplyQuotedText)
	if quoted == "" {
		quoted = "…"
	}
	replyPlain := msg.Text

	var plainQuote string
	if parent.SenderMXID != "" {
		plainQuote = fmt.Sprintf("> <%s> %s", parent.SenderMXID, quoted)
	} else {
		plainQuote = "> " + quoted
	}
	content.Body = plainQuote + "\n\n" + replyPlain

	eventPermalink := matrixToPermalinkRoomEvent(portal.MXID, parent.MXID)
	var formatted strings.Builder
	formatted.WriteString(`<mx-reply><blockquote>`)
	if parent.SenderMXID != "" {
		userPermalink := matrixToPermalinkUser(parent.SenderMXID)
		fmt.Fprintf(&formatted, `<a href="%s">In reply to</a> <a href="%s">%s</a><br>`,
			html.EscapeString(eventPermalink),
			html.EscapeString(userPermalink),
			html.EscapeString(string(parent.SenderMXID)),
		)
	} else {
		fmt.Fprintf(&formatted, `<a href="%s">In reply to a message</a><br>`,
			html.EscapeString(eventPermalink),
		)
	}
	formatted.WriteString(event.TextToHTML(quoted))
	formatted.WriteString(`</blockquote></mx-reply>`)
	formatted.WriteString(event.TextToHTML(replyPlain))

	content.Format = event.FormatHTML
	content.FormattedBody = formatted.String()
	return cm
}

func matrixToPermalinkRoomEvent(roomID id.RoomID, eventID id.EventID) string {
	return fmt.Sprintf("https://matrix.to/#/%s/%s",
		url.PathEscape(string(roomID)),
		url.PathEscape(string(eventID)),
	)
}

func matrixToPermalinkUser(userID id.UserID) string {
	return fmt.Sprintf("https://matrix.to/#/%s", url.PathEscape(string(userID)))
}

// convertVideoMessage downloads a shared TikTok video and uploads it to Matrix
// as an m.video event. Shared TikTok post titles/URLs are attached as the
// media caption so Matrix receives one event instead of separate video + text
// messages.
// If the download or upload fails for any reason, it falls back to a plain-text
// message with the URL so the user can still open it manually.
//
// Download path:
//  1. GET the TikTok video page URL (msg.MediaURL).
//  2. Capture the Set-Cookie headers from that response.
//  3. Parse the embedded __DEFAULT_SCOPE__ JSON to extract:
//     __DEFAULT_SCOPE__["webapp.video-detail"].itemInfo.itemStruct.video.playAddr
//  4. GET the play address using the captured cookies.
//  5. Stream the resulting bytes to Matrix via intent.UploadMediaStream.
func (tc *TikTokClient) convertVideoMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	log := zerolog.Ctx(ctx)

	var (
		data []byte
		mime string
		err  error
	)
	isPrivateVideo := msg.MessageSubtype == "private_video"
	if isPrivateVideo {
		data, mime, err = tc.apiClient.DownloadPrivateVideo(ctx, msg.MediaURL)
	} else {
		data, mime, err = tc.apiClient.DownloadVideo(ctx, msg.MediaURL)
	}
	if err != nil {
		if msg.SharedPost != nil {
			log.Debug().Err(err).Str("url", msg.MediaURL).
				Msg("Failed to download shared TikTok video; sending its cover card instead")
			return tc.convertSharedPostMessage(ctx, portal, intent, msg)
		}
		log.Warn().Err(err).Str("url", msg.MediaURL).
			Msg("Failed to download TikTok video; falling back to text")
		return convertVideoFallback(msg), nil
	}

	// Pick a sensible filename and Matrix message type from the MIME type.
	msgType, fileName := msgTypeAndName(mime)

	size := int64(len(data))
	content := &event.MessageEventContent{
		MsgType: msgType,
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     int(size),
		},
	}
	if msg.MediaWidth > 0 {
		content.Info.Width = msg.MediaWidth
	}
	if msg.MediaHeight > 0 {
		content.Info.Height = msg.MediaHeight
	}
	if msg.MediaDurationMs > 0 {
		content.Info.Duration = msg.MediaDurationMs
	}

	mxcURL, encFile, err := intent.UploadMediaStream(
		ctx,
		portal.MXID,
		size,
		false, // requireFile — an in-memory buffer is fine
		func(dest io.Writer) (*bridgev2.FileStreamResult, error) {
			if _, err := dest.Write(data); err != nil {
				return nil, err
			}
			return &bridgev2.FileStreamResult{
				FileName: fileName,
				MimeType: mime,
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("upload media to Matrix: %w", err)
	}

	// content.URL holds the unencrypted MXC URI; content.File is populated
	// instead when the room is encrypted (bridgev2 handles the distinction).
	content.URL = mxcURL
	content.File = encFile

	if !isPrivateVideo {
		applyTikTokVideoCaption(content, msg)
	}

	vmeta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: vmeta,
			},
		},
	}, nil
}

// convertSharedPostMessage renders a TikTok post shared into a DM the way
// TikTok's own client does: the post's cover image with the author, caption and
// a link to the post as the image caption. It is used for photo-mode posts
// (which have no video to download) and for shared videos whose download
// failed. Without a downloadable cover it falls back to the same text card.
func (tc *TikTokClient) convertSharedPostMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	post := msg.SharedPost
	if post == nil || post.CoverURL == "" || tc.apiClient == nil || intent == nil || portal == nil {
		return convertVideoFallback(msg), nil
	}
	log := zerolog.Ctx(ctx)

	data, mime, err := tc.apiClient.DownloadSharedPostCover(ctx, post.CoverURL)
	if err != nil {
		log.Warn().Err(err).Str("item_id", post.ItemID).
			Msg("Failed to download shared TikTok post cover; falling back to text")
		return convertVideoFallback(msg), nil
	}

	fileName := imageFileName(mime)
	size := int64(len(data))
	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     int(size),
			Width:    post.CoverWidth,
			Height:   post.CoverHeight,
		},
	}

	mxcURL, encFile, err := intent.UploadMediaStream(
		ctx,
		portal.MXID,
		size,
		false,
		func(dest io.Writer) (*bridgev2.FileStreamResult, error) {
			if _, err := dest.Write(data); err != nil {
				return nil, err
			}
			return &bridgev2.FileStreamResult{
				FileName: fileName,
				MimeType: mime,
			}, nil
		},
	)
	if err != nil {
		log.Warn().Err(err).Str("item_id", post.ItemID).
			Msg("Failed to upload shared TikTok post cover; falling back to text")
		return convertVideoFallback(msg), nil
	}
	content.URL = mxcURL
	content.File = encFile
	applyTikTokVideoCaption(content, msg)

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: messageMetaFromLib(msg),
			},
		},
	}, nil
}

// convertVideoFallback renders a TikTok video share as a plain-text message
// containing the URL when media download is unavailable.
func convertVideoFallback(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := tiktokVideoCaptionBody(msg)
	if body == "" {
		body = "[video]"
	}

	fmeta := messageMetaFromLib(msg)
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}
	if formattedBody := tiktokVideoCaptionHTML(msg); formattedBody != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = formattedBody
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: fmeta,
			},
		},
	}
}

// convertStickerFallback renders a sticker as a plain-text notice when the
// CDN asset could not be downloaded or the payload was missing a usable URL.
func convertStickerFallback(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := msg.Text
	if body == "" {
		body = "[sticker]"
	}
	if msg.MediaURL != "" {
		body += "\n" + msg.MediaURL
	}

	meta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    body,
				},
				DBMetadata: meta,
			},
		},
	}
}

// convertImageFallback renders a private image as a plain-text notice when the
// encrypted CDN payload could not be fetched or decrypted.
func convertImageFallback(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := msg.Text
	if body == "" {
		body = "[photo]"
	}

	meta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    body,
				},
				DBMetadata: meta,
			},
		},
	}
}

func applyTikTokVideoCaption(content *event.MessageEventContent, msg libtiktok.Message) {
	body := tiktokVideoCaptionBody(msg)
	if body == "" {
		return
	}
	content.FileName = content.Body
	content.Body = body
	content.Format = event.FormatHTML
	content.FormattedBody = tiktokVideoCaptionHTML(msg)
}

func tiktokVideoCaptionBody(msg libtiktok.Message) string {
	if msg.SharedPost != nil {
		return sharedPostCaptionBody(msg)
	}
	text := strings.TrimSpace(msg.Text)
	mediaURL := strings.TrimSpace(msg.MediaURL)
	switch {
	case text != "" && mediaURL != "":
		return text + "\n" + mediaURL
	case text != "":
		return text
	default:
		return mediaURL
	}
}

func tiktokVideoCaptionHTML(msg libtiktok.Message) string {
	if msg.SharedPost != nil {
		return sharedPostCaptionHTML(msg)
	}
	text := strings.TrimSpace(msg.Text)
	mediaURL := strings.TrimSpace(msg.MediaURL)
	switch {
	case text != "" && mediaURL != "":
		return event.TextToHTML(text) + `<br><a href="` + html.EscapeString(mediaURL) + `">` + html.EscapeString(mediaURL) + `</a>`
	case text != "":
		return event.TextToHTML(text)
	case mediaURL != "":
		return `<a href="` + html.EscapeString(mediaURL) + `">` + html.EscapeString(mediaURL) + `</a>`
	default:
		return ""
	}
}

// sharedPostCaptionBody formats a shared post as a short plain-text card:
//
//	Kay on TikTok
//	<caption>
//	https://www.tiktok.com/@/video/<id>
func sharedPostCaptionBody(msg libtiktok.Message) string {
	post := msg.SharedPost
	lines := []string{sharedPostHeading(post)}
	if post.Caption != "" {
		lines = append(lines, post.Caption)
	}
	if mediaURL := strings.TrimSpace(msg.MediaURL); mediaURL != "" {
		lines = append(lines, mediaURL)
	}
	return strings.Join(lines, "\n")
}

// sharedPostCaptionHTML is the HTML form of sharedPostCaptionBody, with the
// author in bold and the link labelled instead of shown in full.
func sharedPostCaptionHTML(msg libtiktok.Message) string {
	post := msg.SharedPost
	heading := "TikTok post"
	if post.AuthorName != "" {
		heading = "<b>" + html.EscapeString(post.AuthorName) + "</b> on TikTok"
	}
	parts := []string{heading}
	if post.Caption != "" {
		parts = append(parts, event.TextToHTML(post.Caption))
	}
	if mediaURL := strings.TrimSpace(msg.MediaURL); mediaURL != "" {
		parts = append(parts, `<a href="`+html.EscapeString(mediaURL)+`">Open on TikTok</a>`)
	}
	return strings.Join(parts, "<br>")
}

func sharedPostHeading(post *libtiktok.SharedPost) string {
	if post.AuthorName == "" {
		return "TikTok post"
	}
	return post.AuthorName + " on TikTok"
}

// convertUnknownMessage produces a fallback m.notice for message types that
// libtiktok does not yet decode (images, stickers, likes, reactions, etc.).
func convertUnknownMessage(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	var body string
	if strings.TrimSpace(msg.Text) != "" {
		if msg.Type != "" {
			body = fmt.Sprintf("[%s] %s", msg.Type, msg.Text)
		} else if msg.MessageSubtype != "" {
			body = fmt.Sprintf("[subtype %s] %s", msg.MessageSubtype, msg.Text)
		} else {
			body = msg.Text
		}
	} else {
		switch {
		case msg.Type != "":
			body = fmt.Sprintf("[unsupported message type: %s]", msg.Type)
		case msg.MessageSubtype != "":
			body = fmt.Sprintf("[unsupported TikTok message subtype: %s]", msg.MessageSubtype)
		default:
			body = "[unsupported TikTok message]"
		}
	}

	umeta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    body,
				},
				DBMetadata: umeta,
			},
		},
	}
}

// matrixToTikTok converts the body of a Matrix message into the plain-text
// string that TikTok's send API expects. Callers should strip Matrix reply
// fallbacks (e.g. content.RemoveReplyFallback()) before calling when the event
// is a rich reply.
func matrixToTikTok(content *event.MessageEventContent) (string, error) {
	switch content.MsgType {
	case event.MsgText, event.MsgNotice:
		return content.Body, nil
	case event.MsgEmote:
		return "* " + content.Body, nil
	default:
		return "", fmt.Errorf("unsupported Matrix message type for TikTok: %s", content.MsgType)
	}
}

// msgTypeAndName returns the Matrix message type and a default filename for a
// given MIME type. We always download video.playAddr so the result is MsgVideo
// in practice; the MIME type from the server is used only to pick the extension.
func msgTypeAndName(mime string) (event.MessageType, string) {
	switch mime {
	case "video/webm":
		return event.MsgVideo, "video.webm"
	case "video/quicktime":
		return event.MsgVideo, "video.mov"
	default:
		return event.MsgVideo, "video.mp4"
	}
}

func stickerFileName(mime string) string {
	switch mime {
	case "image/png":
		return "sticker.png"
	case "image/gif":
		return "sticker.gif"
	case "image/jpeg":
		return "sticker.jpg"
	default:
		return "sticker.webp"
	}
}

func imageFileName(mime string) string {
	switch mime {
	case "image/png":
		return "image.png"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	case "image/jpeg":
		return "image.jpg"
	default:
		return "image.bin"
	}
}
