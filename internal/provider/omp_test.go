package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListAndReadOMPFiles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(root, "bucket", "child"), 0700); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(root, "bucket", "main.jsonl")
	main := `{"type":"title","v":1,"title":"Current title","source":"user"}
{"type":"session","version":3,"id":"019fb35e-cd1f-72c1-b95f-f265043655ec","timestamp":"2026-09-03T10:00:00Z","cwd":"` + workspace + `","title":"Old title"}
{"type":"message","id":"user0001","parentId":null,"timestamp":"2026-09-03T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}
{"type":"message"`
	if err := os.WriteFile(mainPath, []byte(main), 0600); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root, "bucket", "child", "child.jsonl")
	child := `{"type":"session","version":3,"id":"029fb35e-cd1f-72c1-b95f-f265043655ec","timestamp":"2026-09-03T11:00:00Z","cwd":"` + workspace + `"}
`
	if err := os.WriteFile(childPath, []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bucket", "artifact.jsonl"), []byte(`{"value":1}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	sessions, err := ListOMP(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %#v", sessions)
	}
	var mainSession OMPSession
	for _, session := range sessions {
		if session.ID == "019fb35e-cd1f-72c1-b95f-f265043655ec" {
			mainSession = session
		}
	}
	if mainSession.Title != "Current title" || mainSession.CWD != workspace || mainSession.LastModified <= 0 {
		t.Fatalf("main session = %#v", mainSession)
	}

	file, err := ReadOMP(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if file.Header["title"] != "Current title" || file.Header["titleSource"] != "user" {
		t.Fatalf("header = %#v", file.Header)
	}
	if len(file.Entries) != 1 {
		t.Fatalf("entries = %#v", file.Entries)
	}
}

func TestListOMPMissingRootIsEmpty(t *testing.T) {
	sessions, err := ListOMP(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v", sessions)
	}
}
