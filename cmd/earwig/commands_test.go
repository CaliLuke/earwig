package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CaliLuke/earwig/internal/spool"
)

func TestRootHelpUsesCobraCommandTree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := runCLI(nil, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	for _, want := range []string{
		"Capture Commands:",
		"Inspect Commands:",
		"sessions",
		"Use \"earwig [command] --help\"",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help omitted %q:\n%s", want, stdout.String())
		}
	}
}

func TestVersionDoesNotLoadConfiguration(t *testing.T) {
	t.Setenv("EARWIG_CONFIG", t.TempDir())
	var stdout, stderr bytes.Buffer
	exitCode := runCLI([]string{"version"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), "earwig dev") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
}

func TestCobraRequiredFlagAndSuggestions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := runCLI([]string{"export"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 1 || !strings.Contains(stderr.String(), `required flag(s) "dir" not set`) {
		t.Fatalf("required flag exit=%d stderr=%s", exitCode, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = runCLI([]string{"sesions"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 1 || !strings.Contains(stderr.String(), `Did you mean this?`) ||
		!strings.Contains(stderr.String(), "sessions") {
		t.Fatalf("suggestion exit=%d stderr=%s", exitCode, stderr.String())
	}
}

func TestSessionsHelpDocumentsInspectionFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := runCLI([]string{"sessions", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	for _, want := range []string{"--provider", "--limit", "--warnings", "--json", "without loading or printing"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("sessions help omitted %q:\n%s", want, stdout.String())
		}
	}
}

func TestProviderAndLimitValidation(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"sessions", "--provider", "other"}, "--provider must be claude or codex"},
		{[]string{"sessions", "--limit", "0"}, "--limit must be between 1 and 1000"},
		{[]string{"sweep", "--session", "abc"}, "--session requires --provider"},
	} {
		var stdout, stderr bytes.Buffer
		exitCode := runCLI(test.args, strings.NewReader(""), &stdout, &stderr)
		if exitCode != 1 || !strings.Contains(stderr.String(), test.want) {
			t.Errorf("%v exit=%d stderr=%s", test.args, exitCode, stderr.String())
		}
	}
}

func TestSessionsTablePreservesFullSessionIDs(t *testing.T) {
	var stdout bytes.Buffer
	app := &application{out: &stdout}
	id := "019fb35e-cd1f-72c1-b95f-f265043655ec"
	err := writeSessionsTable(app, sessionsResult{
		Total: 1,
		Sessions: []spool.CapturedSession{{
			Provider:       "codex-app-server",
			SessionID:      id,
			CWD:            "/work/earwig",
			LastCapturedMS: 1000,
			Turns:          7,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Captured sessions (showing 1 of 1)", "Codex", id} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("table omitted %q:\n%s", want, stdout.String())
		}
	}
}

func TestStatusPreservesScriptFriendlyDaemonLine(t *testing.T) {
	var stdout bytes.Buffer
	app := &application{out: &stdout}
	if err := writeStatus(app, statusResult{Healthy: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "daemon: stopped\n") {
		t.Fatalf("status omitted stable daemon line:\n%s", stdout.String())
	}
}
