package autostart

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
)

func spec() Spec {
	return Spec{Profile: "vortx", Executable: "/Users/me/.local/bin/agentclip", HomeDir: "/Users/me", UID: 501, LogPath: "/Users/me/Library/Caches/agentclip/logs/vortx.autostart.log"}
}

func TestPlanForRefusesWhatCouldEscapeAFileNameOrACommand(t *testing.T) {
	for _, profile := range []string{"", "../x", "a b", "a;b", "a/b", `a\b`, "$(x)", strings.Repeat("a", 65), "é"} {
		s := spec()
		s.Profile = profile
		if _, err := PlanFor("linux", s); err == nil {
			t.Errorf("profile %q was accepted", profile)
		}
	}
	relative := spec()
	relative.Executable = "agentclip"
	if _, err := PlanFor("linux", relative); err == nil {
		t.Error("a relative executable path was accepted")
	}
	if _, err := PlanFor("plan9", spec()); err == nil {
		t.Error("an unsupported system was accepted")
	}
}

func TestDarwinPlanIsAValidLaunchAgentThatRunsTheCompanionAtLogin(t *testing.T) {
	plan, err := PlanFor("darwin", spec())
	if err != nil {
		t.Fatal(err)
	}
	file := plan.Files[0]
	if file.Path != "/Users/me/Library/LaunchAgents/com.wendellrocha.agentclip.vortx.plist" {
		t.Errorf("path = %s", file.Path)
	}
	if err := xml.Unmarshal([]byte(file.Content), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("the property list is not well-formed XML: %v\n%s", err, file.Content)
	}
	for _, want := range []string{
		"<string>com.wendellrocha.agentclip.vortx</string>",
		"<string>/Users/me/.local/bin/agentclip</string>\n\t\t<string>companion</string>\n\t\t<string>serve</string>\n\t\t<string>vortx</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>SuccessfulExit</key>\n\t\t<false/>",
	} {
		if !strings.Contains(file.Content, want) {
			t.Errorf("the property list lacks %q:\n%s", want, file.Content)
		}
	}
	if got := plan.Enable[0].String(); got != "launchctl bootstrap gui/501 /Users/me/Library/LaunchAgents/com.wendellrocha.agentclip.vortx.plist" {
		t.Errorf("enable = %s", got)
	}
	if got := plan.Disable[0].String(); got != "launchctl bootout gui/501/com.wendellrocha.agentclip.vortx" {
		t.Errorf("disable = %s", got)
	}
}

// A path with characters that mean something in XML must not break the file or
// smuggle in another argument.
func TestDarwinPlanEscapesThePathsItWrites(t *testing.T) {
	s := spec()
	s.Executable = "/Users/me/A&B <x>/agentclip"
	plan, err := PlanFor("darwin", s)
	if err != nil {
		t.Fatal(err)
	}
	content := plan.Files[0].Content
	if err := xml.Unmarshal([]byte(content), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("not well-formed after escaping: %v", err)
	}
	if !strings.Contains(content, "/Users/me/A&amp;B &lt;x&gt;/agentclip") || strings.Contains(content, "<x>") {
		t.Errorf("the path was not escaped:\n%s", content)
	}
}

func TestLinuxPlanIsASystemdUserUnit(t *testing.T) {
	plan, err := PlanFor("linux", spec())
	if err != nil {
		t.Fatal(err)
	}
	file := plan.Files[0]
	if file.Path != "/Users/me/.config/systemd/user/agentclip-vortx.service" {
		t.Errorf("path = %s", file.Path)
	}
	for _, want := range []string{"ExecStart=/Users/me/.local/bin/agentclip companion serve vortx", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(file.Content, want) {
			t.Errorf("the unit lacks %q:\n%s", want, file.Content)
		}
	}
	var enable []string
	for _, command := range plan.Enable {
		enable = append(enable, command.String())
	}
	if strings.Join(enable, ";") != "systemctl --user daemon-reload;systemctl --user enable --now agentclip-vortx.service" {
		t.Errorf("enable = %v", enable)
	}
}

// systemd splits ExecStart on spaces and expands % and $, so a path holding any
// of them is quoted and escaped rather than trusted.
func TestSystemdQuoteProtectsSpacesAndSpecifiers(t *testing.T) {
	for word, want := range map[string]string{
		"/usr/bin/agentclip":          "/usr/bin/agentclip",
		"/home/me/my tools/agentclip": `"/home/me/my tools/agentclip"`,
		"/opt/100%/agentclip":         `"/opt/100%%/agentclip"`,
		"/opt/$HOME/agentclip":        `"/opt/$$HOME/agentclip"`,
		`/opt/a"b/agentclip`:          `"/opt/a\"b/agentclip"`,
		`/opt/a\b/agentclip`:          `"/opt/a\\b/agentclip"`,
		"":                            `""`,
	} {
		if got := systemdQuote(word); got != want {
			t.Errorf("systemdQuote(%q) = %s, want %s", word, got, want)
		}
	}
}

func TestWindowsPlanCreatesALogonTaskWithoutFiles(t *testing.T) {
	s := spec()
	s.Executable = `C:\Program Files\AgentClip\agentclip.exe`
	plan, err := PlanFor("windows", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 0 || plan.Query == nil {
		t.Fatalf("plan = %+v", plan)
	}
	create := plan.Enable[0].String()
	for _, want := range []string{"/SC ONLOGON", "/RL LIMITED", "/TN com.wendellrocha.agentclip.vortx", `/TR "C:\Program Files\AgentClip\agentclip.exe" companion serve vortx`} {
		if !strings.Contains(create, want) {
			t.Errorf("create task %q lacks %q", create, want)
		}
	}
	if got := plan.Disable[0].String(); !strings.Contains(got, "/Delete /F /TN com.wendellrocha.agentclip.vortx") {
		t.Errorf("delete = %s", got)
	}
}

// fake is a system that remembers what it was told: a service is on after
// `enable`/`bootstrap`/`/Create` and off after the matching disable, unless that
// command is made to fail. It records everything in order.
type fake struct {
	files    map[string]string
	dirs     []string
	commands []string
	on       bool
	failOn   string
}

func (f *fake) runner(goos string) Runner {
	f.files = map[string]string{}
	return Runner{
		GOOS: goos,
		MkdirAll: func(path string) error {
			f.dirs = append(f.dirs, path)
			f.commands = append(f.commands, "mkdir "+path)
			return nil
		},
		WriteFile: func(path string, content []byte) error {
			f.files[path] = string(content)
			f.commands = append(f.commands, "write "+path)
			return nil
		},
		Remove: func(path string) error {
			delete(f.files, path)
			f.commands = append(f.commands, "rm "+path)
			return nil
		},
		Exists: func(path string) bool { _, ok := f.files[path]; return ok },
		Run: func(command Command) error {
			text := command.String()
			f.commands = append(f.commands, text)
			if f.failOn != "" && strings.Contains(text, f.failOn) {
				return errors.New("exit status 1")
			}
			switch {
			case strings.Contains(text, "enable --now") || strings.Contains(text, "bootstrap") || strings.Contains(text, "/Create"):
				f.on = true
			case strings.Contains(text, "disable --now") || strings.Contains(text, "bootout") || strings.Contains(text, "/Delete"):
				f.on = false
			case strings.Contains(text, "is-enabled") || strings.Contains(text, "/Query"):
				if !f.on {
					return errors.New("exit status 1")
				}
			}
			return nil
		},
	}
}

func TestEnableWritesUnloadsTheOldDefinitionThenLoads(t *testing.T) {
	f := &fake{}
	r := f.runner("darwin")
	if err := r.Enable(spec()); err != nil {
		t.Fatal(err)
	}
	if len(f.files) != 1 {
		t.Fatalf("files = %v", f.files)
	}
	var loading []string
	for _, command := range f.commands {
		if strings.HasPrefix(command, "launchctl") {
			loading = append(loading, command)
		}
	}
	if len(loading) != 2 || !strings.HasPrefix(loading[0], "launchctl bootout") || !strings.HasPrefix(loading[1], "launchctl bootstrap") {
		t.Fatalf("commands = %v, want bootout (ignored) then bootstrap so that a second enable reloads", loading)
	}
	if on, err := r.Enabled(spec()); err != nil || !on {
		t.Fatalf("Enabled = %v, %v", on, err)
	}
}

// launchd opens the log files before it starts the process and does not create
// their directory, so on a machine where the Companion never ran the job would
// fail to launch every time.
func TestDarwinEnableCreatesTheLogDirectoryBeforeLoadingTheAgent(t *testing.T) {
	f := &fake{}
	if err := f.runner("darwin").Enable(spec()); err != nil {
		t.Fatal(err)
	}
	if len(f.dirs) != 1 || f.dirs[0] != "/Users/me/Library/Caches/agentclip/logs" {
		t.Fatalf("directories = %v, want the directory of the log", f.dirs)
	}
	mkdir, bootstrap := -1, -1
	for i, command := range f.commands {
		if strings.HasPrefix(command, "mkdir ") {
			mkdir = i
		}
		if strings.HasPrefix(command, "launchctl bootstrap") {
			bootstrap = i
		}
	}
	if mkdir < 0 || bootstrap < 0 || mkdir > bootstrap {
		t.Fatalf("the directory must exist before bootstrap: %v", f.commands)
	}
}

func TestEnableLeavesNothingBehindWhenTheSystemRefusesToStartIt(t *testing.T) {
	f := &fake{failOn: "bootstrap"}
	r := f.runner("darwin")
	err := r.Enable(spec())
	if err == nil || !strings.Contains(err.Error(), "launchctl bootstrap") {
		t.Fatalf("err = %v, want the failing command named", err)
	}
	if len(f.files) != 0 {
		t.Fatalf("a definition was left behind: %v", f.files)
	}
}

func TestDisableStopsRemovesAndCanBeRepeated(t *testing.T) {
	f := &fake{}
	r := f.runner("linux")
	if err := r.Enable(spec()); err != nil {
		t.Fatal(err)
	}
	if on, _ := r.Enabled(spec()); !on {
		t.Fatal("not enabled after Enable")
	}
	if err := r.Disable(spec()); err != nil {
		t.Fatal(err)
	}
	if len(f.files) != 0 {
		t.Fatalf("the unit was not removed: %v", f.files)
	}
	if last := f.commands[len(f.commands)-1]; !strings.Contains(last, "is-enabled") {
		t.Errorf("last command = %q, want the check that it is really off", last)
	}
	if on, _ := r.Enabled(spec()); on {
		t.Error("still enabled after Disable")
	}
	if err := r.Disable(spec()); err != nil {
		t.Fatalf("disabling twice failed: %v", err)
	}
}

// Turning off what the system refuses to turn off must not report success: a
// task that stays registered would start at every login.
func TestDisableReportsAServiceTheSystemWillNotStop(t *testing.T) {
	for goos, failing := range map[string]string{"windows": "/Delete", "linux": "disable --now"} {
		t.Run(goos, func(t *testing.T) {
			f := &fake{}
			r := f.runner(goos)
			if err := r.Enable(spec()); err != nil {
				t.Fatal(err)
			}
			f.failOn = failing
			err := r.Disable(spec())
			if err == nil || !strings.Contains(err.Error(), "still on") || !strings.Contains(err.Error(), failing) {
				t.Fatalf("err = %v, want it to say autostart is still on and why", err)
			}
		})
	}
	// Nothing to turn off is not a failure, even though the command fails.
	f := &fake{failOn: "/Delete"}
	if err := f.runner("windows").Disable(spec()); err != nil {
		t.Fatalf("disabling what was never on failed: %v", err)
	}
}

// After `systemctl disable` the unit file is still there, but the login no
// longer starts it: the status follows the system, not the file.
func TestLinuxStatusFollowsWhetherTheSystemWillStartIt(t *testing.T) {
	f := &fake{}
	r := f.runner("linux")
	if err := r.Enable(spec()); err != nil {
		t.Fatal(err)
	}
	f.on = false // disabled behind our back; the file remains
	if len(f.files) != 1 {
		t.Fatal("the unit file should still exist")
	}
	if on, _ := r.Enabled(spec()); on {
		t.Error("reported as on although the system will not start it")
	}
}

func TestWindowsEnabledAsksTheTaskScheduler(t *testing.T) {
	f := &fake{}
	r := f.runner("windows")
	if on, _ := r.Enabled(spec()); on {
		t.Fatal("Enabled although no task exists")
	}
	if err := r.Enable(spec()); err != nil {
		t.Fatal(err)
	}
	if on, err := r.Enabled(spec()); err != nil || !on {
		t.Fatalf("Enabled = %v, %v, want on once the task exists", on, err)
	}
}

// The service does not inherit the shell that enabled it, so what the profile
// lookup depends on is written into the definition.
func TestEnvironmentIsWrittenIntoTheServiceDefinition(t *testing.T) {
	s := spec()
	s.Environment = map[string]string{"AGENTCLIP_CONFIG_DIR": "/data/my config & more/%h"}
	darwin, err := PlanFor("darwin", s)
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal([]byte(darwin.Files[0].Content), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("plist: %v", err)
	}
	if !strings.Contains(darwin.Files[0].Content, "<key>EnvironmentVariables</key>") || !strings.Contains(darwin.Files[0].Content, "<key>AGENTCLIP_CONFIG_DIR</key>\n\t\t<string>/data/my config &amp; more/%h</string>") {
		t.Errorf("plist lacks the environment:\n%s", darwin.Files[0].Content)
	}
	linux, err := PlanFor("linux", s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(linux.Files[0].Content, `Environment="AGENTCLIP_CONFIG_DIR=/data/my config & more/%%h"`) {
		t.Errorf("unit lacks the quoted environment (specifiers doubled):\n%s", linux.Files[0].Content)
	}
	if _, err := PlanFor("windows", s); err == nil || !strings.Contains(err.Error(), "AGENTCLIP_CONFIG_DIR") {
		t.Errorf("a Windows task cannot carry the environment; err = %v", err)
	}
	plain, _ := PlanFor("darwin", spec())
	if strings.Contains(plain.Files[0].Content, "EnvironmentVariables") {
		t.Error("an empty environment must not add a section")
	}
}
