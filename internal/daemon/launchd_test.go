package daemon

import (
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlistHasWorkingDirectoryAndEscapesPaths(t *testing.T) {
	plist := Plist("/Applications/Earwig & Tools/earwig")
	if err := xml.Unmarshal([]byte(plist), new(any)); err != nil {
		t.Fatalf("invalid plist XML: %v", err)
	}
	for _, want := range []string{
		"<string>/Applications/Earwig &amp; Tools/earwig</string>",
		"<key>WorkingDirectory</key><string>/Applications/Earwig &amp; Tools</string>",
		"<key>PATH</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist omitted %q: %s", want, plist)
		}
	}
}

func TestSystemdUnitHasRestartPolicyAndEscapesSpecifiers(t *testing.T) {
	unit := SystemdUnit("/opt/Earwig 100%/earwig")
	for _, want := range []string{
		`ExecStart="/opt/Earwig 100%%/earwig" watch`,
		`WorkingDirectory="/opt/Earwig 100%%"`,
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit omitted %q: %s", want, unit)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/earwig-config")
	if got := SystemdPath(); got != filepath.Join("/tmp/earwig-config", "systemd", "user", "earwig.service") {
		t.Fatalf("systemd path = %q", got)
	}
}
