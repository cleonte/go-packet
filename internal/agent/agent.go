// Package agent installs and removes the launchd user agent for `packet daemon`. `packet agent install` runs the daemon at login through a launchd user agent.
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const agentLabel = "local.packet"

func agentPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library/LaunchAgents", agentLabel+".plist")
}

func CmdAgent(args []string) int {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	switch {
	case len(args) == 1 && args[0] == "install":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "packet:", err)
			return 1
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
</dict></plist>
`, agentLabel, exe)
		path := agentPlistPath()
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "packet:", err)
			return 1
		}
		_ = exec.Command("launchctl", "bootout", domain+"/"+agentLabel).Run() // fine if not loaded
		if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "packet: launchctl: %s\n", out)
			return 1
		}
		fmt.Println("installed", path)
		return 0
	case len(args) == 1 && args[0] == "uninstall":
		_ = exec.Command("launchctl", "bootout", domain+"/"+agentLabel).Run()
		if err := os.Remove(agentPlistPath()); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "packet:", err)
			return 1
		}
		fmt.Println("uninstalled")
		return 0
	}
	fmt.Fprintln(os.Stderr, "agent needs install or uninstall")
	return 2
}
