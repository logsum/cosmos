package ui

import (
	"github.com/charmbracelet/bubbles/key"
)

// KeyMap holds the key bindings for the scaffold.
type KeyMap struct {
	SwitchTabRight key.Binding
	SwitchTabLeft  key.Binding
	Quit           key.Binding
}

func newKeyMap() *KeyMap {
	return &KeyMap{
		SwitchTabRight: key.NewBinding(
			key.WithKeys("ctrl+right"),
		),
		SwitchTabLeft: key.NewBinding(
			key.WithKeys("ctrl+left"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c", "ctrl+d"),
		),
	}
}
