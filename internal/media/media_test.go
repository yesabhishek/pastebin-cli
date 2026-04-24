package media

import "testing"

func TestNormalizeImagePath(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"screenshots/capture":      "screenshots/capture.png",
		"screenshots/capture.png":  "screenshots/capture.png",
		"screenshots/capture.JPG":  "screenshots/capture.JPG",
		"screenshots/capture.webp": "screenshots/capture.webp",
	}
	for input, want := range tests {
		if got := NormalizeImagePath(input); got != want {
			t.Fatalf("NormalizeImagePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestImageContentType(t *testing.T) {
	t.Parallel()

	if got := ImageContentType([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}); got != ContentTypePNG {
		t.Fatalf("expected PNG content type, got %q", got)
	}
	if got := ImageContentType([]byte("plain text")); got != "" {
		t.Fatalf("expected non-image content type, got %q", got)
	}
}
