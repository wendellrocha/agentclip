package main

import (
	"strings"

	"github.com/wendellrocha/agentclip/internal/i18n"
	"github.com/wendellrocha/agentclip/internal/setup"
)

// command describes one command of the CLI. The table below is the single source
// of what exists: main dispatches from it and the help is written from it, so the
// two cannot drift apart.
type command struct {
	name    string
	aliases []string
	group   string
	// summary is one line for the list of commands.
	summary string
	// usage is the syntax, one line per form. It is not translated.
	usage []string
	// details explains what the command does and its options.
	details string
	run     func(arguments []string) error
}

const (
	groupSetup       = "setup"
	groupCompanion   = "companion"
	groupMaintenance = "maintenance"
	groupAdvanced    = "advanced"
)

// groupOrder is the order help lists the groups in.
var groupOrder = []string{groupSetup, groupCompanion, groupMaintenance, groupAdvanced}

func groupTitle(group string) string {
	switch group {
	case groupSetup:
		return i18n.T("Set up a server")
	case groupCompanion:
		return i18n.T("The local Companion")
	case groupMaintenance:
		return i18n.T("Maintenance")
	default:
		return i18n.T("Advanced and internal")
	}
}

// commandTable builds the commands in the language of the process. It is built
// when help or a dispatch needs it, after i18n.Init has run.
func commandTable() []command {
	return []command{
		{
			name: "setup", group: groupSetup, run: runSetup,
			summary: i18n.T("Install and configure a server in one step"),
			usage:   []string{strings.TrimPrefix(setup.Usage, "usage: ")},
			details: i18n.T(`The recommended way to set up a server. It installs the matching release on the
Linux or macOS server over SSH, creates or replaces the local profile, detects
the coding-agent harnesses that are already installed, registers AgentClip with
each of them and starts the local Companion.

  --profile NAME     local name of the pairing (default: derived from the destination)
  --agent NAME       limit the setup to one harness; "all" is the default
  --version vX.Y.Z   release to install on the server
  --remote-port N    loopback port on the server used by the reverse tunnel
  --skip-agent       do not register the MCP integration on the server
  --skip-install     do not install or update the binary on the server
  --no-start         finish without starting the Companion

If SSH still asks for a password, setup creates a private AgentClip key and
asks for the password once to authorize it. Running setup again for the same
profile issues a new pairing token and stops the previous Companion.`),
		},
		{
			name: "pair", group: groupSetup, run: runPair,
			summary: i18n.T("Create a local pairing and configure the harnesses on a server"),
			usage:   []string{"agentclip pair <profile> <ssh-destination> [--agent NAME] [--remote-port N] [--skip-agent]"},
			details: i18n.T(`Creates only the local pairing and configures the harnesses on the server. Useful
for development, or when agentclip was already installed on the server by hand.
Unlike setup, it does not install the remote binary and does not start the
Companion: start it with "agentclip companion start <profile>".`),
		},
		{
			name: "connect", group: groupSetup, run: runConnect,
			summary: i18n.T("Register AgentClip again with the harnesses of a saved profile"),
			usage:   []string{"agentclip connect <profile> [--agent NAME]"},
			details: i18n.T(`Reads an existing profile and registers AgentClip again with the harnesses on
the server. It does not restart the Companion or change the profile's token or
port.`),
		},
		{
			name: "uninstall", aliases: []string{"disconnect"}, group: groupSetup, run: runUninstall,
			summary: i18n.T("Remove AgentClip's MCP entry from one harness on a server"),
			usage:   []string{"agentclip uninstall <profile> --agent NAME"},
			details: i18n.T(`Removes only AgentClip's MCP entry or extension from the chosen harness on the
server. It does not remove the harness, the remote binary, the local profile or
a running Companion. "disconnect" is an alias.`),
		},
		{
			name: "companion", group: groupCompanion, run: runCompanion,
			summary: i18n.T("Run and manage the local Companion"),
			usage: []string{
				"agentclip companion <start|stop|status|open|view|run|inbox> <profile> [--verbose]",
				"agentclip companion <accept|reject> <profile> <offer-id>",
				"agentclip companion autostart <enable|disable|status> <profile>",
			},
			details: i18n.T(`The Companion follows your clipboard, keeps the local bridge running and holds
the reverse SSH tunnel to the server.

  start      start the Companion in the background
  stop       stop it cleanly and close the tunnel
  status     print its state as JSON: tunnel, clipboard items, update notice
  open       open its local web page in your browser ("view" is the same)
  run        run in the foreground, for diagnosis
  inbox      show files the server has offered and what was received
  accept     approve a pending file offer without opening the browser
  reject     refuse a pending file offer
  autostart  start it when you log in: "enable", "disable" or "status"

Add --verbose to start or run to log clipboard and tunnel events (never tokens
or content). The autostart uses a LaunchAgent on macOS, a systemd user unit on
Linux and a Task Scheduler task on Windows.`),
		},
		{
			name: "logs", group: groupCompanion, run: runLogs,
			summary: i18n.T("Show the path of a Companion's log, or export it"),
			usage:   []string{"agentclip logs <profile> [--export PATH]"},
			details: i18n.T(`Prints the path of the Companion's private log. With --export it copies the
whole log to PATH instead. To capture more detail while reproducing a problem,
restart the profile with "agentclip companion start <profile> --verbose".`),
		},
		{
			name: "upgrade", group: groupMaintenance, run: runUpgrade,
			summary: i18n.T("Update this machine and every saved server to the latest release"),
			usage:   []string{"agentclip upgrade"},
			details: i18n.T(`Downloads the latest stable release, checks its SHA-256 and its build
attestation, replaces this executable and updates every saved server: this
machine downloads and verifies the binary for the server's platform and sends
it over SSH. A server that is already up to date is left alone, and only the
Companions that were running are restarted.

Set AGENTCLIP_SKIP_ATTESTATION=1 to skip the attestation check.`),
		},
		{
			name: "doctor", group: groupMaintenance, run: func([]string) error { return runDoctor() },
			summary: i18n.T("Check that a local bridge is healthy"),
			usage:   []string{"agentclip doctor"},
			details: i18n.T(`Checks for a healthy local bridge and prints its address and process ID. For the
Companion workflow prefer "agentclip companion status <profile>", which also
reports the tunnel and the clipboard.`),
		},
		{
			name: "help", aliases: []string{"-h", "--help"}, group: groupMaintenance, run: runHelp,
			summary: i18n.T("Show the list of commands, or the help of one command"),
			usage:   []string{"agentclip help [command]", "agentclip <command> --help"},
			details: i18n.T(`Without arguments, lists every command with a one-line summary. With a command,
explains it: its syntax, what it does and its options. Asking for help never
runs the command.`),
		},
		{
			name: "version", group: groupMaintenance, run: func([]string) error { printVersion(); return nil },
			aliases: []string{"--version", "-v"},
			summary: i18n.T("Print the version"),
			usage:   []string{"agentclip version"},
			details: i18n.T(`Prints the version of this binary. The installers use it to decide whether an
update is needed.`),
		},
		{
			name: "arm", group: groupAdvanced, run: func([]string) error { return runArm() },
			summary: i18n.T("Arm an image from the clipboard for the standalone flow"),
			usage:   []string{"agentclip arm"},
			details: i18n.T(`Captures an image from the clipboard and arms it on the local bridge for 90
seconds, for the standalone flow with "agentclip ssh". The Companion is the
modern path, and it handles images, text and files.`),
		},
		{
			name: "ssh", group: groupAdvanced, run: runSSH,
			summary: i18n.T("Open an SSH session that shares the armed image"),
			usage:   []string{"agentclip ssh <ssh-destination> [-- <ssh arguments>]"},
			details: i18n.T(`Opens a temporary SSH session for the image armed by "agentclip arm" and
forwards a reverse tunnel only for the length of that session. Arguments after
"--" go to ssh unchanged.`),
		},
		{
			name: "mcp", group: groupAdvanced, run: func([]string) error { return runMCP() },
			summary: i18n.T("Serve the MCP tools over stdio (run by the harness)"),
			usage:   []string{"agentclip mcp"},
			details: i18n.T(`Runs the MCP server on standard input and output. A harness starts it; you do
not run it yourself.`),
		},
		{
			name: "harness", group: groupAdvanced, run: runHarness,
			summary: i18n.T("Install or remove the integration of a harness (used by setup)"),
			usage:   []string{"agentclip harness <install|remove> <agy|opencode|pi> --name NAME [--port PORT --token TOKEN]"},
			details: i18n.T(`Writes or removes AgentClip's integration for the harnesses that have no MCP
command of their own (AGY, OpenCode and Pi). Setup, pair and connect call it on
the server; you rarely need to.`),
		},
		{
			name: "bridge", group: groupAdvanced, run: func([]string) error { return runBridge() },
			summary: i18n.T("Run the local bridge on its own (internal)"),
			usage:   []string{"agentclip bridge"},
			details: i18n.T(`Runs the local bridge without a Companion. It is used internally; the Companion
starts the bridge for you.`),
		},
	}
}
