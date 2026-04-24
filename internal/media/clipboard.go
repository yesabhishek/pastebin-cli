package media

import "errors"

var (
	ErrClipboardUnavailable = errors.New("clipboard unavailable")
	ErrNoClipboardImage     = errors.New("no image found on clipboard")
	ErrUnsupportedClipboard = errors.New("unsupported clipboard content")
)

type Clipboard interface {
	ReadImage() ([]byte, error)
	WriteImage([]byte) error
	WriteText([]byte) error
}

type SystemClipboard struct{}
