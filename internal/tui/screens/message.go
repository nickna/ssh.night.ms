package screens

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Message is a one-line model used for fatal/notice states (e.g., the
// transport layer landed in the program handler without a Known decision).
// Quits the session on any key press.
type Message struct{ Text string }

func NewMessage(text string) tea.Model { return Message{Text: text} }

func (m Message) Init() tea.Cmd { return nil }

func (m Message) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, tea.Quit
	}
	return m, nil
}

func (m Message) View() string { return m.Text + "\n\n(press any key to exit)\n" }
