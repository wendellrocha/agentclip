// Package autostart makes the Companion of a profile start when the user logs
// in, using what each system already has: a LaunchAgent on macOS, a systemd user
// unit on Linux and a Task Scheduler task on Windows. It only builds the files
// and commands; the effects are injected through Runner, so every platform can
// be tested from any machine.
package autostart

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
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
	// Environment is passed to the service. A login service does not inherit the
	// shell that enabled it, so anything the profile lookup depends on, such as
	// AGENTCLIP_CONFIG_DIR, has to be written into the definition.
	Environment map[string]string
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
	// Dirs are created before anything is loaded: the service manager opens the
	// log files before it starts the process and does not create their directory.
	Dirs  []string
	Files []File
	// Enable runs after the files are written.
	Enable []Command
	// Disable runs before the files are removed. Failures are ignored, since the
	// service may simply not be loaded.
	Disable []Command
	// AfterRemove runs once the files are gone.
	AfterRemove []Command
	// Query asks the system whether autostart is on. When nil, the plan is on if
	// its first file exists, which is what makes a LaunchAgent load at login.
	Query *Command
}

var profileName = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

// PlanFor builds the plan for goos. The profile name is restricted to the
// characters AgentClip allows, since it becomes part of a file name and a label.
func PlanFor(goos string, spec Spec) (Plan, error) {
	if !profileName.MatchString(spec.Profile) {
		return Plan{}, fmt.Errorf("invalid Companion profile %q", spec.Profile)
	}
	// The plan is for goos, whichever system builds it, so paths follow goos's
	// rules and not the host's: a macOS plan uses slashes even when built on Windows.
	if goos != "windows" && !path.IsAbs(spec.Executable) {
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
		if len(spec.Environment) > 0 {
			return Plan{}, errors.New("a Windows logon task cannot carry environment variables such as AGENTCLIP_CONFIG_DIR; unset them, or set them for your user account, and turn autostart on again")
		}
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
	file := path.Join(spec.HomeDir, "Library", "LaunchAgents", label+".plist")
	domain := fmt.Sprintf("gui/%d", spec.UID)
	arguments := append([]string{spec.Executable}, serveArguments(spec)...)
	var program strings.Builder
	for _, argument := range arguments {
		program.WriteString("\t\t<string>" + xmlEscape(argument) + "</string>\n")
	}
	logPath := xmlEscape(spec.LogPath)
	environment := ""
	if len(spec.Environment) > 0 {
		var entries strings.Builder
		for _, key := range sortedKeys(spec.Environment) {
			entries.WriteString("\t\t<key>" + xmlEscape(key) + "</key>\n\t\t<string>" + xmlEscape(spec.Environment[key]) + "</string>\n")
		}
		environment = "\t<key>EnvironmentVariables</key>\n\t<dict>\n" + entries.String() + "\t</dict>\n"
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(label) + `</string>
	<key>ProgramArguments</key>
	<array>
` + program.String() + `	</array>
` + environment + `	<key>RunAtLoad</key>
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
		Dirs:    []string{path.Dir(spec.LogPath)},
		Files:   []File{{Path: file, Content: content}},
		Enable:  []Command{{"launchctl", []string{"bootstrap", domain, file}}},
		Disable: []Command{{"launchctl", []string{"bootout", domain + "/" + label}}},
	}
}

func linuxPlan(spec Spec) Plan {
	unit := "agentclip-" + spec.Profile + ".service"
	file := path.Join(spec.HomeDir, ".config", "systemd", "user", unit)
	words := make([]string, 0, 4)
	for _, argument := range append([]string{spec.Executable}, serveArguments(spec)...) {
		words = append(words, systemdQuote(argument))
	}
	content := `[Unit]
Description=AgentClip Companion (` + spec.Profile + `)
After=network-online.target

[Service]
ExecStart=` + strings.Join(words, " ") + `
` + systemdEnvironment(spec.Environment) + `Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
	return Plan{
		Files:       []File{{Path: file, Content: content}},
		Enable:      []Command{{"systemctl", []string{"--user", "daemon-reload"}}, {"systemctl", []string{"--user", "enable", "--now", unit}}},
		Disable:     []Command{{"systemctl", []string{"--user", "disable", "--now", unit}}},
		AfterRemove: []Command{{"systemctl", []string{"--user", "daemon-reload"}}},
		// Enabled means the login will start it, which is not the same as the
		// unit file being there: `systemctl disable` leaves the file.
		Query: &Command{"systemctl", []string{"--user", "is-enabled", "--quiet", unit}},
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

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// systemdEnvironment renders Environment= lines, one quoted assignment each.
func systemdEnvironment(values map[string]string) string {
	var lines strings.Builder
	for _, key := range sortedKeys(values) {
		lines.WriteString("Environment=" + systemdQuote(key+"="+values[key]) + "\n")
	}
	return lines.String()
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
	// MkdirAll creates a directory and its parents, privately.
	MkdirAll func(path string) error
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
	for _, dir := range plan.Dirs {
		if err := r.MkdirAll(dir); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
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
// not on succeeds, so it can be run to make sure. What the system refuses to
// stop is not hidden: when it still reports autostart as on afterwards, the
// error of the command that should have turned it off is returned.
func (r Runner) Disable(spec Spec) error {
	plan, err := PlanFor(r.GOOS, spec)
	if err != nil {
		return err
	}
	var lastFailure error
	for _, command := range plan.Disable {
		if err := r.Run(command); err != nil {
			lastFailure = fmt.Errorf("%s: %w", command, err)
		}
	}
	for _, file := range plan.Files {
		if err := r.Remove(file.Path); err != nil {
			return fmt.Errorf("remove %s: %w", file.Path, err)
		}
	}
	for _, command := range plan.AfterRemove {
		_ = r.Run(command)
	}
	if plan.Query != nil && r.Run(*plan.Query) == nil {
		if lastFailure == nil {
			lastFailure = errors.New("the system still reports it as on")
		}
		return fmt.Errorf("autostart is still on: %w", lastFailure)
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
