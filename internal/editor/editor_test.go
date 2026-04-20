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

type fakeSaver struct {
	saveCalls     int
	recoveryCalls int
	clearCalls    int
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
