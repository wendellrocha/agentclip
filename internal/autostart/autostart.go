// Package autostart makes the Companion of a profile start when the user logs
// in, using what each system already has: a LaunchAgent on macOS, a systemd user
// unit on Linux and a Task Scheduler task on Windows. It only builds the files
// and commands; the effects are injected through Runner, so every platform can
// be tested from any machine.
package autostart

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Spec describes what to start.
type Spec struct {
	Profile    string
	Executable string
	// HomeDir is the user's home directory, where the agent and unit files live.
	HomeDir string
	// UID is the numeric user id, which launchctl needs for the gui domain.
	UID int
	// LogPath receives what the process writes before its own log exists.
	LogPath string
}

// Command is one process to run.
type Command struct {
	Name string
	Args []string
}

func (c Command) String() string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

// File is a file to write.
type File struct {
	Path    string
	Content string
}

// Plan is everything one platform needs to turn autostart on and off.
type Plan struct {
	Files []File
	// Enable runs after the files are written.
	Enable []Command
	// Disable runs before the files are removed. Failures are ignored, since the
	// service may simply not be loaded.
	Disable []Command
	// AfterRemove runs once the files are gone.
	AfterRemove []Command
	// Query succeeds when autostart is on. When empty, the plan is on if its
	// first file exists.
	Query *Command
}

var profileName = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

// PlanFor builds the plan for goos. The profile name is restricted to the
// characters AgentClip allows, since it becomes part of a file name and a label.
func PlanFor(goos string, spec Spec) (Plan, error) {
	if !profileName.MatchString(spec.Profile) {
		return Plan{}, fmt.Errorf("invalid Companion profile %q", spec.Profile)
	}
	if !filepath.IsAbs(spec.Executable) && goos != "windows" {
		return Plan{}, errors.New("the AgentClip executable path must be absolute")
	}
	if spec.HomeDir == "" && goos != "windows" {
		return Plan{}, errors.New("the home directory is required")
	}
	switch goos {
	case "darwin":
		return darwinPlan(spec), nil
	case "linux":
		return linuxPlan(spec), nil
	case "windows":
		return windowsPlan(spec), nil
	default:
		return Plan{}, fmt.Errorf("autostart is not supported on %s", goos)
	}
}

func serveArguments(spec Spec) []string { return []string{"companion", "serve", spec.Profile} }

// Label is the LaunchAgent label and the name of the Windows task.
func Label(profile string) string { return "com.wendellrocha.agentclip." + profile }

func darwinPlan(spec Spec) Plan {
	label := Label(spec.Profile)
	path := filepath.Join(spec.HomeDir, "Library", "LaunchAgents", label+".plist")
	domain := fmt.Sprintf("gui/%d", spec.UID)
	arguments := append([]string{spec.Executable}, serveArguments(spec)...)
	var program strings.Builder
	for _, argument := range arguments {
		program.WriteString("\t\t<string>" + xmlEscape(argument) + "</string>\n")
	}
	logPath := xmlEscape(spec.LogPath)
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(label) + `</string>
	<key>ProgramArguments</key>
	<array>
` + program.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + logPath + `</string>
	<key>StandardErrorPath</key>
	<string>` + logPath + `</string>
</dict>
</plist>
`
	return Plan{
		Files:   []File{{Path: path, Content: content}},
		Enable:  []Command{{"launchctl", []string{"bootstrap", domain, path}}},
		Disable: []Command{{"launchctl", []string{"bootout", domain + "/" + label}}},
	}
}

func linuxPlan(spec Spec) Plan {
	unit := "agentclip-" + spec.Profile + ".service"
	path := filepath.Join(spec.HomeDir, ".config", "systemd", "user", unit)
	words := make([]string, 0, 4)
	for _, argument := range append([]string{spec.Executable}, serveArguments(spec)...) {
		words = append(words, systemdQuote(argument))
	}
	content := `[Unit]
Description=AgentClip Companion (` + spec.Profile + `)
After=network-online.target

[Service]
ExecStart=` + strings.Join(words, " ") + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
	return Plan{
		Files:       []File{{Path: path, Content: content}},
		Enable:      []Command{{"systemctl", []string{"--user", "daemon-reload"}}, {"systemctl", []string{"--user", "enable", "--now", unit}}},
		Disable:     []Command{{"systemctl", []string{"--user", "disable", "--now", unit}}},
		AfterRemove: []Command{{"systemctl", []string{"--user", "daemon-reload"}}},
	}
}

func windowsPlan(spec Spec) Plan {
	task := Label(spec.Profile)
	command := `"` + spec.Executable + `" ` + strings.Join(serveArguments(spec), " ")
	return Plan{
		Enable:  []Command{{"schtasks", []string{"/Create", "/F", "/SC", "ONLOGON", "/RL", "LIMITED", "/TN", task, "/TR", command}}},
		Disable: []Command{{"schtasks", []string{"/Delete", "/F", "/TN", task}}},
		Query:   &Command{"schtasks", []string{"/Query", "/TN", task}},
	}
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}

// systemdQuote quotes one word of an ExecStart line. systemd expands % and $ and
// splits on spaces, so a path containing any of them must be escaped, not trusted.
func systemdQuote(word string) string {
	if word != "" && !strings.ContainsAny(word, " \t\n\"'\\%$;") {
		return word
	}
	word = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(word)
	return `"` + word + `"`
}

// Runner applies a Plan. Every effect is a field, so tests can watch what would
// happen; the commands wire them to the real system.
type Runner struct {
	GOOS string
	// WriteFile writes the file, creating its directory.
	WriteFile func(path string, content []byte) error
	// Remove deletes the file; a file that is already gone is not an error.
	Remove func(path string) error
	Exists func(path string) bool
	Run    func(Command) error
}

// Enable writes the files and turns autostart on. It is safe to run again: an
// already loaded service is unloaded first, so the new definition takes effect.
// When starting fails, what was written is removed again.
func (r Runner) Enable(spec Spec) error {
	plan, err := PlanFor(r.GOOS, spec)
	if err != nil {
		return err
	}
	for _, file := range plan.Files {
		if err := r.WriteFile(file.Path, []byte(file.Content)); err != nil {
			return fmt.Errorf("write %s: %w", file.Path, err)
		}
	}
	for _, command := range plan.Disable {
		_ = r.Run(command)
	}
	for _, command := range plan.Enable {
		if err := r.Run(command); err != nil {
			for _, file := range plan.Files {
				_ = r.Remove(file.Path)
			}
			return fmt.Errorf("%s: %w", command, err)
		}
	}
	return nil
}

// Disable turns autostart off and removes what Enable wrote. Turning off what is
// not on succeeds, so it can be run to make sure.
func (r Runner) Disable(spec Spec) error {
	plan, err := PlanFor(r.GOOS, spec)
	if err != nil {
		return err
	}
	for _, command := range plan.Disable {
		_ = r.Run(command)
	}
	for _, file := range plan.Files {
		if err := r.Remove(file.Path); err != nil {
			return fmt.Errorf("remove %s: %w", file.Path, err)
		}
	}
	for _, command := range plan.AfterRemove {
		_ = r.Run(command)
	}
	return nil
}

// Enabled reports whether autostart is on for the profile.
func (r Runner) Enabled(spec Spec) (bool, error) {
	plan, err := PlanFor(r.GOOS, spec)
	if err != nil {
		return false, err
	}
	if plan.Query != nil {
		return r.Run(*plan.Query) == nil, nil
	}
	return len(plan.Files) > 0 && r.Exists(plan.Files[0].Path), nil
}
