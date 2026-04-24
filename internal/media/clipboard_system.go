package media

import (
	"errors"
	"fmt"

	xclipboard "golang.design/x/clipboard"
)

func (SystemClipboard) ReadImage() ([]byte, error) {
	if err := xclipboard.Init(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrClipboardUnavailable, err)
	}
	data := xclipboard.Read(xclipboard.FmtImage)
	if len(data) == 0 {
		return nil, ErrNoClipboardImage
	}
	return append([]byte(nil), data...), nil
}

func (SystemClipboard) WriteImage(data []byte) error {
	if ImageContentType(data) != ContentTypePNG {
		return fmt.Errorf("%w: image clipboard writes require PNG content", ErrUnsupportedClipboard)
	}
	if err := xclipboard.Init(); err != nil {
		return fmt.Errorf("%w: %v", ErrClipboardUnavailable, err)
	}
	if changed := xclipboard.Write(xclipboard.FmtImage, data); changed == nil {
		return errors.New("write image to clipboard failed")
	}
	return nil
}

func (SystemClipboard) WriteText(data []byte) error {
	if err := xclipboard.Init(); err != nil {
		return fmt.Errorf("%w: %v", ErrClipboardUnavailable, err)
	}
	if changed := xclipboard.Write(xclipboard.FmtText, data); changed == nil {
		return errors.New("write text to clipboard failed")
	}
	return nil
}
