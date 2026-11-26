package libtiktok

import (
	"strings"
)

// Shared-post aweType values seen in DM content JSON.
const (
	aweTypeSharedVideo     = 800 // a TikTok video shared into a DM
	aweTypeSharedPhotoPost = 810 // a TikTok photo-mode (carousel) post shared into a DM
	aweTypeGIFSticker      = 502 // a GIF picked from the keyboard's GIF search
)

// SharedPost describes a TikTok post (video or photo-mode carousel) that was
// shared into a DM. TikTok's own client renders these as a card with the
// post's cover, its author and its caption; the bridge uses the same fields
// so Matrix clients can show something close to that instead of bare text.
type SharedPost struct {
	// ItemID is the post's aweme/item id (content JSON "itemId").
	ItemID string
	// AuthorUID is the numeric user id of the post's author (content JSON "uid").
	AuthorUID string
	// AuthorName is the author's display name as TikTok shows it on the card
	// (content JSON "content_name"). It is not the @username.
	AuthorName string
	// Caption is the post's description (content JSON "content_title").
	Caption string
	// CoverURL is a signed, unauthenticated CDN URL for the post's cover image.
	CoverURL    string
	CoverWidth  int
	CoverHeight int
	// IsPhotoPost is true for photo-mode posts (aweType 810), which have no
	// playable video to download.
	IsPhotoPost bool
}

// sharedPostURL returns a link that opens the shared post on tiktok.com.
// TikTok's canonical URL includes the author's @username, which the DM payload
// does not carry. When the username is unknown, the empty-handle form
// "https://www.tiktok.com/@/video/<id>" is used; tiktok.com resolves it to the
// post regardless of the handle.
func sharedPostURL(uniqueID, itemID string) string {
	if itemID == "" {
		return ""
	}
	return "https://www.tiktok.com/@" + strings.TrimPrefix(uniqueID, "@") + "/video/" + itemID
}

// parseSharedPostFromContentJSON extracts the shared-post card fields from a
// decoded content JSON object. It returns nil when the object is not a shared
// video (aweType 800) or photo post (aweType 810), or has no item id.
func parseSharedPostFromContentJSON(content map[string]any) *SharedPost {
	aweType, ok := content["aweType"].(float64)
	if !ok {
		return nil
	}
	switch int(aweType) {
	case aweTypeSharedVideo, aweTypeSharedPhotoPost:
	default:
		return nil
	}
	itemID := jsonStringFromAny(content["itemId"])
	if itemID == "" {
		return nil
	}
	post := &SharedPost{
		ItemID:      itemID,
		AuthorUID:   jsonStringFromAny(content["uid"]),
		AuthorName:  strings.TrimSpace(jsonStringFromAny(content["content_name"])),
		Caption:     strings.TrimSpace(jsonStringFromAny(content["content_title"])),
		CoverURL:    firstURLFromImageObject(content["cover_url"]),
		CoverWidth:  intFromJSONNumber(content["cover_width"]),
		CoverHeight: intFromJSONNumber(content["cover_height"]),
		IsPhotoPost: int(aweType) == aweTypeSharedPhotoPost,
	}
	return post
}

// parseSharedPostFromContentBytes is parseSharedPostFromContentJSON for raw
// content JSON bytes, as carried on the wire.
func parseSharedPostFromContentBytes(contentBytes []byte) *SharedPost {
	if len(contentBytes) == 0 {
		return nil
	}
	content, err := parseContentJSONObject(contentBytes)
	if err != nil {
		return nil
	}
	return parseSharedPostFromContentJSON(content)
}

// parseGIFStickerFromContentJSON extracts the image URL from an aweType 502
// payload (a GIF chosen from the keyboard's GIF search). The image lives under
// url.url_list; display_name is a short label such as "GIF".
func parseGIFStickerFromContentJSON(content map[string]any) (mediaURL, text string, ok bool) {
	mediaURL = firstURLFromImageObject(content["url"])
	if mediaURL == "" {
		return "", "", false
	}
	return mediaURL, strings.TrimSpace(jsonStringFromAny(content["display_name"])), true
}

// firstURLFromImageObject returns the first https URL from a TikTok image
// object of the form {"uri": "...", "url_list": ["https://...", ...]}.
func firstURLFromImageObject(v any) string {
	obj, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	list, ok := obj["url_list"].([]any)
	if !ok {
		return ""
	}
	for _, item := range list {
		if s, ok := item.(string); ok && strings.HasPrefix(s, "https://") {
			return s
		}
	}
	return ""
}

func intFromJSONNumber(v any) int {
	if f, ok := v.(float64); ok && f > 0 {
		return int(f)
	}
	return 0
}
