package daemon

import (
	"encoding/xml"
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
