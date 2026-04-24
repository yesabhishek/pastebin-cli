package media

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	_ "golang.org/x/image/webp"
)

func PNGForClipboard(data []byte) ([]byte, error) {
	if ImageContentType(data) == ContentTypePNG {
		return append([]byte(nil), data...), nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: decode image for clipboard: %v", ErrUnsupportedClipboard, err)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("encode clipboard PNG: %w", err)
	}
	return out.Bytes(), nil
}
