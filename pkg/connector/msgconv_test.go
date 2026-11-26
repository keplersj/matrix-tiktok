package connector

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

func TestConvertMessage_ignoresPlaceholderCompanion(t *testing.T) {
	tc := &TikTokClient{}
	msg := libtiktok.Message{
		Type:           "",
		MessageSubtype: "",
		RawContentJSON: []byte(`{"hack":"1"}`),
	}
	_, err := tc.convertMessage(context.Background(), nil, nil, msg)
	if !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) {
		t.Fatalf("convertMessage: err = %v, want ErrIgnoringRemoteEvent", err)
	}
}

func TestConvertMessage_doesNotIgnoreFailedPrivateImage(t *testing.T) {
	tc := &TikTokClient{}
	msg := libtiktok.Message{
		Type:           "",
		MessageSubtype: "private_image",
		RawContentJSON: []byte(`{"hack":"1"}`),
	}
	cm, err := tc.convertMessage(context.Background(), nil, nil, msg)
	if err != nil {
		t.Fatalf("convertMessage: %v", err)
	}
	if cm == nil || len(cm.Parts) != 1 {
		t.Fatalf("expected one converted part, got %+v", cm)
	}
}

func TestApplyTikTokVideoCaption(t *testing.T) {
	content := &event.MessageEventContent{
		MsgType: event.MsgVideo,
		Body:    "video.mp4",
	}
	msg := libtiktok.Message{
		Type:     "video",
		Text:     "A < B",
		MediaURL: "https://www.tiktok.com/@user/video/123?x=1&y=2",
	}

	applyTikTokVideoCaption(content, msg)

	if content.FileName != "video.mp4" {
		t.Fatalf("FileName = %q, want original body as filename", content.FileName)
	}
	wantBody := "A < B\nhttps://www.tiktok.com/@user/video/123?x=1&y=2"
	if content.Body != wantBody {
		t.Fatalf("Body = %q, want %q", content.Body, wantBody)
	}
	if content.Format != event.FormatHTML {
		t.Fatalf("Format = %q, want HTML", content.Format)
	}
	wantHTML := `A &lt; B<br><a href="https://www.tiktok.com/@user/video/123?x=1&amp;y=2">https://www.tiktok.com/@user/video/123?x=1&amp;y=2</a>`
	if content.FormattedBody != wantHTML {
		t.Fatalf("FormattedBody = %q, want %q", content.FormattedBody, wantHTML)
	}
}

func syntheticSharedPostMessage() libtiktok.Message {
	return libtiktok.Message{
		Type:     "shared_post",
		Text:     "Weekend <photos>",
		MediaURL: "https://www.tiktok.com/@/video/7000000000000000001",
		SharedPost: &libtiktok.SharedPost{
			ItemID:      "7000000000000000001",
			AuthorName:  "Example & Co",
			Caption:     "Weekend <photos>",
			CoverURL:    "https://p16.example.tiktokcdn.com/photomode/1.jpeg",
			IsPhotoPost: true,
		},
	}
}

func TestConvertMessage_sharedPostWithoutCoverDownloadIsTextCard(t *testing.T) {
	tc := &TikTokClient{}
	cm, err := tc.convertMessage(context.Background(), nil, nil, syntheticSharedPostMessage())
	if err != nil {
		t.Fatalf("convertMessage: %v", err)
	}
	if cm == nil || len(cm.Parts) != 1 {
		t.Fatalf("expected one converted part, got %+v", cm)
	}
	content := cm.Parts[0].Content
	if content.MsgType != event.MsgText {
		t.Fatalf("MsgType = %q, want m.text", content.MsgType)
	}
	wantBody := "Example & Co on TikTok\nWeekend <photos>\nhttps://www.tiktok.com/@/video/7000000000000000001"
	if content.Body != wantBody {
		t.Fatalf("Body = %q, want %q", content.Body, wantBody)
	}
	wantHTML := `<b>Example &amp; Co</b> on TikTok<br>Weekend &lt;photos&gt;<br><a href="https://www.tiktok.com/@/video/7000000000000000001">Open on TikTok</a>`
	if content.FormattedBody != wantHTML {
		t.Fatalf("FormattedBody = %q, want %q", content.FormattedBody, wantHTML)
	}
}

func TestApplyTikTokVideoCaption_sharedPostWithoutAuthor(t *testing.T) {
	msg := syntheticSharedPostMessage()
	msg.SharedPost.AuthorName = ""
	msg.SharedPost.Caption = ""
	content := &event.MessageEventContent{MsgType: event.MsgImage, Body: "image.jpg"}

	applyTikTokVideoCaption(content, msg)

	if content.FileName != "image.jpg" {
		t.Fatalf("FileName = %q", content.FileName)
	}
	if want := "TikTok post\nhttps://www.tiktok.com/@/video/7000000000000000001"; content.Body != want {
		t.Fatalf("Body = %q, want %q", content.Body, want)
	}
}
