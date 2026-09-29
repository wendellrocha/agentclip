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

// fake records effects in order and can be told to fail.
type fake struct {
	files    map[string]string
	commands []string
	failOn   string
}

func (f *fake) runner(goos string) Runner {
	f.files = map[string]string{}
	return Runner{
		GOOS:      goos,
		WriteFile: func(path string, content []byte) error { f.files[path] = string(content); return nil },
		Remove: func(path string) error {
			delete(f.files, path)
			f.commands = append(f.commands, "rm "+path)
			return nil
		},
		Exists: func(path string) bool { _, ok := f.files[path]; return ok },
		Run: func(command Command) error {
			f.commands = append(f.commands, command.String())
			if f.failOn != "" && strings.Contains(command.String(), f.failOn) {
				return errors.New("exit status 1")
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
	if len(f.commands) != 2 || !strings.HasPrefix(f.commands[0], "launchctl bootout") || !strings.HasPrefix(f.commands[1], "launchctl bootstrap") {
		t.Fatalf("commands = %v, want bootout (ignored) then bootstrap so that a second enable reloads", f.commands)
	}
	if on, err := r.Enabled(spec()); err != nil || !on {
		t.Fatalf("Enabled = %v, %v", on, err)
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

func TestDisableStopsRemovesAndSucceedsWhenNothingWasOn(t *testing.T) {
	f := &fake{failOn: "disable"}
	r := f.runner("linux")
	if err := r.Enable(spec()); err != nil {
		t.Fatal(err)
	}
	if err := r.Disable(spec()); err != nil {
		t.Fatalf("Disable failed although stopping is best effort: %v", err)
	}
	if len(f.files) != 0 {
		t.Fatalf("the unit was not removed: %v", f.files)
	}
	last := f.commands[len(f.commands)-1]
	if last != "systemctl --user daemon-reload" {
		t.Errorf("last command = %q, want a reload after removing the unit", last)
	}
	if on, _ := r.Enabled(spec()); on {
		t.Error("still enabled after Disable")
	}
	if err := r.Disable(spec()); err != nil {
		t.Fatalf("disabling twice failed: %v", err)
	}
}

func TestWindowsEnabledAsksTheTaskScheduler(t *testing.T) {
	f := &fake{}
	r := f.runner("windows")
	if on, err := r.Enabled(spec()); err != nil || !on {
		t.Fatalf("Enabled = %v, %v, want on when the query succeeds", on, err)
	}
	f.failOn = "/Query"
	if on, _ := r.Enabled(spec()); on {
		t.Fatal("Enabled although the task does not exist")
	}
}
