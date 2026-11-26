package libtiktok

import (
	"context"
	"testing"
)

const syntheticSharedPhotoPostJSON = `{
	"aweType": 810,
	"content_name": "Example Creator",
	"content_thumb": {"uri": "avt/1", "url_list": ["https://p16.example.tiktokcdn.com/avt/1.jpeg"]},
	"content_title": "Weekend photo dump #travel",
	"cover_height": 1920,
	"cover_url": {"uri": "photomode/1", "url_list": ["https://p16.example.tiktokcdn.com/photomode/1.jpeg", "https://p19.example.tiktokcdn.com/photomode/1.jpeg"]},
	"cover_width": 1080,
	"itemId": "7000000000000000001",
	"uid": "1000000000000000001"
}`

func TestParseMessageContent_sharedPhotoPostAweType810(t *testing.T) {
	msgType, text, mediaURL, _ := parseMessageContent(context.Background(), nil, []byte(syntheticSharedPhotoPostJSON))
	if msgType != "shared_post" {
		t.Fatalf("msgType = %q, want shared_post", msgType)
	}
	if text != "Weekend photo dump #travel" {
		t.Fatalf("text = %q", text)
	}
	if want := "https://www.tiktok.com/@/video/7000000000000000001"; mediaURL != want {
		t.Fatalf("mediaURL = %q, want %q", mediaURL, want)
	}
}

func TestParseMessageContent_sharedVideoWithoutUsernameStillLinks(t *testing.T) {
	body := []byte(`{"aweType":800,"itemId":"7000000000000000002","uid":"1000000000000000002","content_title":"caption"}`)
	msgType, text, mediaURL, _ := parseMessageContent(context.Background(), nil, body)
	if msgType != "video" || text != "caption" {
		t.Fatalf("got type=%q text=%q, want video/caption", msgType, text)
	}
	if want := "https://www.tiktok.com/@/video/7000000000000000002"; mediaURL != want {
		t.Fatalf("mediaURL = %q, want %q", mediaURL, want)
	}
}

func TestParseMessageContent_gifStickerAweType502(t *testing.T) {
	body := []byte(`{"aweType":502,"display_name":"GIF","image_type":"gif","sticker_id":"t:s:1","url":{"uri":"","url_list":["https://media.example.com/cat.webp"]},"width":194,"height":194}`)
	msgType, text, mediaURL, mimeType := parseMessageContent(context.Background(), nil, body)
	if msgType != "sticker" {
		t.Fatalf("msgType = %q, want sticker", msgType)
	}
	if mediaURL != "https://media.example.com/cat.webp" || mimeType != "image/webp" {
		t.Fatalf("mediaURL=%q mime=%q", mediaURL, mimeType)
	}
	if text != "[GIF]" {
		t.Fatalf("text = %q, want [GIF]", text)
	}
}

func TestParseMessageContent_gifStickerWithoutURLStaysUnsupported(t *testing.T) {
	msgType, _, _, _ := parseMessageContent(context.Background(), nil, []byte(`{"aweType":502,"display_name":"GIF"}`))
	if msgType != "type_502" {
		t.Fatalf("msgType = %q, want type_502", msgType)
	}
}

func TestParseSharedPostFromContentBytes(t *testing.T) {
	post := parseSharedPostFromContentBytes([]byte(syntheticSharedPhotoPostJSON))
	if post == nil {
		t.Fatal("expected a shared post")
	}
	if post.ItemID != "7000000000000000001" || post.AuthorUID != "1000000000000000001" {
		t.Fatalf("ids = %q/%q", post.ItemID, post.AuthorUID)
	}
	if post.AuthorName != "Example Creator" || post.Caption != "Weekend photo dump #travel" {
		t.Fatalf("author=%q caption=%q", post.AuthorName, post.Caption)
	}
	if post.CoverURL != "https://p16.example.tiktokcdn.com/photomode/1.jpeg" {
		t.Fatalf("cover = %q", post.CoverURL)
	}
	if post.CoverWidth != 1080 || post.CoverHeight != 1920 || !post.IsPhotoPost {
		t.Fatalf("cover dims %dx%d photo=%v", post.CoverWidth, post.CoverHeight, post.IsPhotoPost)
	}
}

func TestParseSharedPostFromContentBytes_ignoresOtherTypes(t *testing.T) {
	for _, body := range []string{
		`{"aweType":0,"text":"hi"}`,
		`{"aweType":800}`,
		`{"hack":"1"}`,
		``,
	} {
		if post := parseSharedPostFromContentBytes([]byte(body)); post != nil {
			t.Fatalf("parseSharedPostFromContentBytes(%q) = %+v, want nil", body, post)
		}
	}
}

func TestSharedPostForType(t *testing.T) {
	if sharedPostForType("sticker", []byte(syntheticSharedPhotoPostJSON)) != nil {
		t.Fatal("sticker rows must not carry a shared post")
	}
	if sharedPostForType("shared_post", []byte(syntheticSharedPhotoPostJSON)) == nil {
		t.Fatal("shared_post rows must carry a shared post")
	}
}

func TestSharedPostURL(t *testing.T) {
	if got := sharedPostURL("creator", "1"); got != "https://www.tiktok.com/@creator/video/1" {
		t.Fatalf("got %q", got)
	}
	if got := sharedPostURL("@creator", "1"); got != "https://www.tiktok.com/@creator/video/1" {
		t.Fatalf("got %q", got)
	}
	if got := sharedPostURL("", ""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
