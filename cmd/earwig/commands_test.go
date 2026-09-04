package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/normalizer"
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

func TestOpikExportHelpDocumentsSelectiveExport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := runCLI([]string{"export", "opik", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	for _, want := range []string{
		"--url", "--project", "--session", "repeat for multiple sessions",
		"unique prefixes", "does not capture new data", "unrelated pending sessions",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("Opik export help omitted %q:\n%s", want, stdout.String())
		}
	}
}

func TestOpikExportRequiresURLAndSessions(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"export", "opik", "--session", "019fb35e"}, `required flag(s) "url" not set`},
		{[]string{"export", "opik", "--url", "https://opik.example.test"}, `required flag(s) "session" not set`},
		{[]string{"export", "opik", "--url", "ssh://opik.example.test", "--session", "019fb35e"}, "exporter URL must be HTTP(S)"},
		{[]string{"export", "opik", "--url", "https://opik.example.test", "--session", "abc"}, "at least 4 characters"},
	} {
		assertCLIError(t, test.args, test.want)
	}
}

func TestOpikExportSendsOnlySelectedCapturedSessions(t *testing.T) {
	store, err := spool.Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SpoolPath = store.Path
	var stdout, stderr bytes.Buffer
	app := &application{
		in:    strings.NewReader(""),
		out:   &stdout,
		err:   &stderr,
		cfg:   &cfg,
		spool: store,
	}
	defer app.close()

	sessionIDs := []string{
		"019fb35e-cd1f-72c1-b95f-f265043655ec",
		"029fb35e-cd1f-72c1-b95f-f265043655ec",
		"039fb35e-cd1f-72c1-b95f-f265043655ec",
	}
	for index, sessionID := range sessionIDs {
		transcript := normalizer.Transcript{
			SchemaVersion: 1,
			Source:        "codex-app-server",
			Session: map[string]any{
				"id":         sessionID,
				"cwd":        "/work/project",
				"name":       "Session " + sessionID[:8],
				"created_at": int64(1000 + index),
				"updated_at": int64(2000 + index),
			},
			Turns: []normalizer.Turn{{
				ID:          "turn-" + sessionID[:8],
				Status:      "completed",
				StartedAt:   int64(1000 + index),
				CompletedAt: int64(1100 + index),
			}},
		}
		if _, err = store.UpsertTranscript(transcript, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	var exportedSessions []string
	var exportedProjects []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var trace map[string]any
		_ = json.Unmarshal(body, &trace)
		exportedSessions = append(exportedSessions, trace["thread_id"].(string))
		exportedProjects = append(exportedProjects, trace["project_name"].(string))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	root := newRootCommand(app)
	root.SetArgs([]string{
		"export", "opik",
		"--url", server.URL,
		"--project", "review",
		"--session", sessionIDs[0][:8],
		"--session", sessionIDs[2],
		"--session", sessionIDs[0],
	})
	if err = root.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	if strings.Join(exportedSessions, ",") != sessionIDs[0]+","+sessionIDs[2] {
		t.Fatalf("exported sessions = %#v", exportedSessions)
	}
	if strings.Join(exportedProjects, ",") != "review,review" {
		t.Fatalf("exported projects = %#v", exportedProjects)
	}
	if !strings.Contains(stdout.String(), `Exported 2 turns from 2 sessions to Opik project "review".`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	checkpoints, err := store.ExportCount("opik")
	if err != nil || checkpoints != 0 {
		t.Fatalf("explicit export changed automatic checkpoints: count=%d err=%v", checkpoints, err)
	}
}

func TestSessionsHelpDocumentsInspectionFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := runCLI([]string{"sessions", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	for _, want := range []string{
		"--provider", "--project", "--search", "--limit", "--warnings", "--long", "--json",
		"show", "without loading or printing",
	} {
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
		{[]string{"sessions", "--provider", "other"}, "--provider must be claude, codex, or omp"},
		{[]string{"sessions", "--limit", "0"}, "--limit must be between 1 and 1000"},
		{[]string{"sessions", "show", "abc"}, "at least 4 characters"},
		{[]string{"sweep", "--session", "abc"}, "--session requires --provider"},
		{[]string{"sweep", "--provider", "omp", "--session", "abc"}, "invalid omp session ID"},
	} {
		assertCLIError(t, test.args, test.want)
	}
}

func assertCLIError(t *testing.T, args []string, want string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exitCode := runCLI(args, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 1 || !strings.Contains(stderr.String(), want) {
		t.Errorf("%v exit=%d stderr=%s", args, exitCode, stderr.String())
	}
}

func TestSessionsTablePrioritizesHumanRecognizableMetadata(t *testing.T) {
	var stdout bytes.Buffer
	app := &application{out: &stdout}
	id := "019fb35e-cd1f-72c1-b95f-f265043655ec"
	err := writeSessionsTable(app, sessionsResult{
		Total:       1,
		GapWarnings: 1,
		Sessions: []spool.CapturedSession{{
			Provider:       "codex-app-server",
			SessionID:      id,
			IDPrefix:       "019fb35e",
			CWD:            "/work/earwig",
			Summary:        "Set up Earwig\nlocally",
			LastActivityMS: time.Now().Add(-9 * time.Minute).UnixMilli(),
			Turns:          7,
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 session · 1 warning", "PROJECT", "TITLE", "Set up Earwig locally", "earwig", "019fb35e"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("table omitted %q:\n%s", want, stdout.String())
		}
	}
	for _, unwanted := range []string{"/work/earwig", id, "STATE"} {
		if strings.Contains(stdout.String(), unwanted) {
			t.Errorf("default table included %q:\n%s", unwanted, stdout.String())
		}
	}
}

func TestLongSessionsTablePreservesFullMetadata(t *testing.T) {
	var stdout bytes.Buffer
	app := &application{out: &stdout}
	id := "019fb35e-cd1f-72c1-b95f-f265043655ec"
	err := writeSessionsTable(app, sessionsResult{
		Total: 1,
		Sessions: []spool.CapturedSession{{
			Provider:       "codex-app-server",
			SessionID:      id,
			CWD:            "/work/earwig",
			Summary:        "Set up Earwig locally",
			LastActivityMS: 1785428000000,
			LastCapturedMS: 1785429000000,
			Turns:          7,
		}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ACTIVITY", "CAPTURED", "STATE", "/work/earwig", id} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("long table omitted %q:\n%s", want, stdout.String())
		}
	}
}

func TestSessionDetailsExposeDrillDownMetadata(t *testing.T) {
	var stdout bytes.Buffer
	app := &application{out: &stdout}
	err := writeSessionDetails(app, spool.CapturedSession{
		Provider:       "claude-code-agent-sdk",
		SessionID:      "3f6ec84f-8ab9-42a3-a59c-ef4d38dac252",
		CWD:            "/work/autok-server",
		Summary:        "Investigate capture gap",
		LastActivityMS: time.Now().Add(-time.Hour).UnixMilli(),
		Turns:          7,
		Compactions:    1,
		HasGapWarning:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Investigate capture gap", "source: Claude", "project: autok-server",
		"turns: 7", "state: capture gap warning",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("details omitted %q:\n%s", want, stdout.String())
		}
	}
}

func TestActivityAgeUsesCompactRelativeUnits(t *testing.T) {
	now := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{9 * time.Minute, "9m"},
		{3 * time.Hour, "3h"},
		{12 * 24 * time.Hour, "12d"},
		{90 * 24 * time.Hour, "3mo"},
	} {
		got := formatActivityAge(now.Add(-test.ago).UnixMilli(), now)
		if got != test.want {
			t.Errorf("%s ago = %q, want %q", test.ago, got, test.want)
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
