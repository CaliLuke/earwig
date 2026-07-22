package daemon

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
func installLaunchd(binary string) error {
	p := PlistPath()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	if e := os.WriteFile(p, []byte(Plist(binary)), 0600); e != nil {
		return e
	}
	return exec.Command("launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), p).Run()
}
func uninstallLaunchd() error {
	p := PlistPath()
	_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/com.caliluke.earwig").Run()
	if e := os.Remove(p); e != nil && !os.IsNotExist(e) {
		return e
	}
	return nil
}

func SystemdUnit(binary string) string {
	binary = filepath.Clean(binary)
	workingDirectory := filepath.Dir(binary)
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	quote := func(value string) string { return strconv.Quote(strings.ReplaceAll(value, "%", "%%")) }
	return fmt.Sprintf(`[Unit]
Description=Earwig agent transcript capture daemon
After=network-online.target

[Service]
Type=simple
ExecStart=%s watch
WorkingDirectory=%s
Environment=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, quote(binary), quote(workingDirectory), quote("PATH="+path))
}

func SystemdPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", "earwig.service")
}

func ServiceDefinition(binary string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return Plist(binary), nil
	case "linux":
		return SystemdUnit(binary), nil
	default:
		return "", fmt.Errorf("service installation is unsupported on %s", runtime.GOOS)
	}
}

func Install(binary string) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(binary)
	case "linux":
		path := SystemdPath()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(SystemdUnit(binary)), 0600); err != nil {
			return err
		}
		if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
			return err
		}
		return exec.Command("systemctl", "--user", "enable", "--now", "earwig.service").Run()
	default:
		return fmt.Errorf("service installation is unsupported on %s", runtime.GOOS)
	}
}

func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		path := SystemdPath()
		_ = exec.Command("systemctl", "--user", "disable", "--now", "earwig.service").Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return exec.Command("systemctl", "--user", "daemon-reload").Run()
	default:
		return fmt.Errorf("service installation is unsupported on %s", runtime.GOOS)
	}
}
