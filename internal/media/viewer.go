package media

import (
	"fmt"
	"os/exec"
	"runtime"
)

type Viewer interface {
	Open(string) error
}

type SystemViewer struct{}

func (SystemViewer) Open(filePath string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", filePath)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", filePath)
	default:
		cmd = exec.Command("xdg-open", filePath)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open image viewer: %w", err)
	}
	return nil
}
