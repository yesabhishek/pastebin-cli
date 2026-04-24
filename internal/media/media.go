package media

import (
	"bytes"
	"mime"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	ContentTypePNG  = "image/png"
	ContentTypeJPEG = "image/jpeg"
	ContentTypeGIF  = "image/gif"
	ContentTypeWEBP = "image/webp"
)

var imageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
}

func NormalizeImagePath(filePath string) string {
	if IsImageExtension(filePath) {
		return filePath
	}
	return filePath + ".png"
}

func IsImageExtension(filePath string) bool {
	return imageExtensions[strings.ToLower(path.Ext(filePath))]
}

func IsImage(filePath string, data []byte) bool {
	return ImageContentType(data) != "" || IsImageExtension(filePath)
}

func ContentType(filePath string, data []byte) string {
	if contentType := ImageContentType(data); contentType != "" {
		return contentType
	}
	if extType := mime.TypeByExtension(strings.ToLower(path.Ext(filePath))); extType != "" {
		return extType
	}
	if IsText(data) {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

func ImageContentType(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return ContentTypePNG
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return ContentTypeJPEG
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return ContentTypeGIF
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ContentTypeWEBP
	default:
		return ""
	}
}

func IsText(data []byte) bool {
	return utf8.Valid(data) && !bytes.Contains(data, []byte{0})
}
