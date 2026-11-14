package cli

import (
	"os"
	"strings"
	"testing"
)

func TestUsageListsEveryFlag(t *testing.T) {
	u := Usage("cernly")
	for _, want := range []string{
		"--config", "--commit", "--committed-at", "--confirmations", "--stack",
		"plugin keygen --out prefix [--test]", "plugin sign", "[--out dir]", "--trust-key", "--format text|json|sarif",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("usage lacks %q:\n%s", want, u)
		}
	}
}

func TestIsHelp(t *testing.T) {
	for _, a := range []string{"help", "-h", "--help"} {
		if !IsHelp([]string{a}) {
			t.Errorf("%s is not help", a)
		}
	}
	if IsHelp([]string{"scan"}) || IsHelp(nil) {
		t.Error("scan or nothing is help")
	}
}

// -h prints a command's flags to stdout; a parse error keeps stderr.
func TestNewFlagSetHelpOutput(t *testing.T) {
	for args, want := range map[string]*os.File{"-h": os.Stdout, "--help": os.Stdout, "--bogus": os.Stderr, "-- -h": os.Stderr} {
		if got := NewFlagSet("scan", strings.Fields(args)).Output(); got != want {
			t.Errorf("%s: output %v, want %v", args, got, want)
		}
	}
}
