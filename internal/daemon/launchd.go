package daemon

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

func Plist(binary string) string {
	binary = filepath.Clean(binary)
	workingDirectory := filepath.Dir(binary)
	path := os.Getenv("PATH")
	if path == "" {
		path = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>com.caliluke.earwig</string><key>ProgramArguments</key><array><string>%s</string><string>watch</string></array><key>WorkingDirectory</key><string>%s</string><key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict><key>RunAtLoad</key><true/><key>KeepAlive</key><true/></dict></plist>
`, xmlText(binary), xmlText(workingDirectory), xmlText(path))
}

func xmlText(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}
func PlistPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", "com.caliluke.earwig.plist")
}
func Install(binary string) error {
	p := PlistPath()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	if e := os.WriteFile(p, []byte(Plist(binary)), 0600); e != nil {
		return e
	}
	return exec.Command("launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), p).Run()
}
func Uninstall() error {
	p := PlistPath()
	_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/com.caliluke.earwig").Run()
	if e := os.Remove(p); e != nil && !os.IsNotExist(e) {
		return e
	}
	return nil
}
