package ssh

import (
	"strings"
	"testing"
)

func TestCommandHelpListsEveryCommand(t *testing.T) {
	help := commandHelp()
	for _, cmd := range commands {
		if !strings.Contains(help, cmd.name) {
			t.Errorf("help is missing %q", cmd.name)
		}
	}
	if !strings.Contains(help, "verify <file>") {
		t.Error("help should show each command's argument")
	}
}
