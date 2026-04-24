package editor

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAutosaveTickWritesRecoveryOnly(t *testing.T) {
	t.Parallel()

	saver := &fakeSaver{}
	model := New("pb editor", "notes/a.txt", "", saver, "", false)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(Model)

	updated, cmd := model.Update(autosaveTick{})
	model = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected autosave command")
	}
	msg := cmd()
	switch typed := msg.(type) {
	case tea.BatchMsg:
		for _, nested := range typed {
			if nested == nil {
				continue
			}
			nestedMsg := nested()
			updated, _ = model.Update(nestedMsg)
			model = updated.(Model)
		}
	default:
		updated, _ = model.Update(msg)
		model = updated.(Model)
	}

	if saver.saveCalls != 0 {
		t.Fatalf("expected no durable save on autosave, got %d", saver.saveCalls)
	}
	if saver.recoveryCalls != 1 {
		t.Fatalf("expected one local recovery save, got %d", saver.recoveryCalls)
	}
}

func TestRecoveredEditorDoesNotDropRecoveryOnCleanQuit(t *testing.T) {
	t.Parallel()

	saver := &fakeSaver{}
	model := New("pb editor", "notes/a.txt", "draft", saver, "Recovered local draft autosave", true)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	model = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected quit command")
	}
	if saver.clearCalls != 0 {
		t.Fatalf("expected recovery draft to be preserved on clean quit from recovered buffer")
	}
}

func TestCtrlXSavesDirtyBufferBeforeQuit(t *testing.T) {
	t.Parallel()

	saver := &fakeSaver{}
	model := New("pb editor", "notes/a.txt", "", saver, "", false)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(Model)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	model = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected save command")
	}
	msg := cmd()
	if _, ok := msg.(saveDoneMsg); !ok {
		t.Fatalf("expected save done message, got %T", msg)
	}
	_, quitCmd := model.Update(msg)
	if saver.saveCalls != 1 {
		t.Fatalf("expected one save before Ctrl+X quit, got %d", saver.saveCalls)
	}
	if quitCmd == nil {
		t.Fatalf("expected quit command after save")
	}
}

func TestCtrlVPastesImageWhenBufferIsClean(t *testing.T) {
	t.Parallel()

	saver := &fakeSaver{}
	model := New("pb editor", "shots/capture", "", saver, "", false)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected paste image command")
	}
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if saver.pasteImageCalls != 1 {
		t.Fatalf("expected one image paste, got %d", saver.pasteImageCalls)
	}
	if model.path != "shots/capture.png" {
		t.Fatalf("expected editor path to update to pasted image path, got %q", model.path)
	}
}

func TestCtrlVDoesNotPasteOverDirtyText(t *testing.T) {
	t.Parallel()

	saver := &fakeSaver{}
	model := New("pb editor", "shots/capture", "", saver, "", false)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(Model)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model = updated.(Model)
	if cmd != nil {
		t.Fatalf("did not expect paste command for dirty text buffer")
	}
	if saver.pasteImageCalls != 0 {
		t.Fatalf("expected no image paste, got %d", saver.pasteImageCalls)
	}
	if model.status == "" {
		t.Fatalf("expected status explaining why paste was skipped")
	}
}

type fakeSaver struct {
	saveCalls       int
	recoveryCalls   int
	clearCalls      int
	pasteImageCalls int
}

func (f *fakeSaver) Save(context.Context, string) (SaveResult, error) {
	f.saveCalls++
	return SaveResult{Path: "notes/a.txt", Message: "saved"}, nil
}

func (f *fakeSaver) SaveRecovery(context.Context, string) error {
	f.recoveryCalls++
	return nil
}

func (f *fakeSaver) ClearRecovery() error {
	f.clearCalls++
	return nil
}

func (f *fakeSaver) PasteImage(context.Context) (SaveResult, error) {
	f.pasteImageCalls++
	return SaveResult{Path: "shots/capture.png", Message: "Image pasted and saved"}, nil
}
