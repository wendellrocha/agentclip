package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wendellrocha/agentclip/internal/buildinfo"
	"github.com/wendellrocha/agentclip/internal/i18n"
)

// findCommand looks a command up by name or alias.
func findCommand(table []command, name string) (command, bool) {
	for _, candidate := range table {
		if candidate.name == name {
			return candidate, true
		}
		for _, alias := range candidate.aliases {
			if alias == name {
				return candidate, true
			}
		}
	}
	return command{}, false
}

// printHelp writes the overview: what AgentClip is, then every command with its
// summary, grouped, and how to get more.
func printHelp(w io.Writer, table []command) {
	fmt.Fprintln(w, i18n.T("AgentClip gives a coding agent on an SSH server access to your clipboard, with your approval."))
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("Usage:"))
	fmt.Fprintln(w, "  agentclip <command> [arguments]")
	fmt.Fprintln(w, "  agentclip help [command]")
	width := 0
	for _, c := range table {
		if len(c.name) > width {
			width = len(c.name)
		}
	}
	for _, group := range groupOrder {
		var lines []string
		for _, c := range table {
			if c.group == group {
				lines = append(lines, fmt.Sprintf("  %-*s  %s", width, c.name, c.summary))
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s:\n%s\n", groupTitle(group), strings.Join(lines, "\n"))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T(`Run "agentclip help <command>" for the details of a command.`))
	fmt.Fprintln(w, i18n.T("Set %s=pt-BR to see the messages in Brazilian Portuguese.", i18n.Env))
}

// printCommandHelp writes the syntax and the explanation of one command.
func printCommandHelp(w io.Writer, c command) {
	fmt.Fprintf(w, "%s\n\n", c.summary)
	fmt.Fprintln(w, i18n.T("Usage:"))
	for _, line := range c.usage {
		fmt.Fprintf(w, "  %s\n", line)
	}
	if len(c.aliases) > 0 && !strings.HasPrefix(c.aliases[0], "-") {
		fmt.Fprintf(w, "\n%s %s\n", i18n.T("Also known as:"), strings.Join(c.aliases, ", "))
	}
	fmt.Fprintf(w, "\n%s\n", c.details)
}

// usageError is a mistake in how the command line was written, which exits with
// status 2 instead of the 1 of a command that failed.
type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

func unknownCommand(name string) usageError {
	return usageError{i18n.T("unknown command %q; run \"agentclip help\" to list the commands", name)}
}

// runHelp is the help command: the overview, or the help of one command.
func runHelp(arguments []string) error {
	table := commandTable()
	if len(arguments) == 0 {
		printHelp(os.Stdout, table)
		return nil
	}
	selected, found := findCommand(table, arguments[0])
	if !found {
		return unknownCommand(arguments[0])
	}
	printCommandHelp(os.Stdout, selected)
	return nil
}

// wantsHelp reports whether the arguments after a command ask for its help.
func wantsHelp(arguments []string) bool {
	return len(arguments) == 1 && (arguments[0] == "-h" || arguments[0] == "--help" || arguments[0] == "help")
}

func printVersion() { fmt.Println(buildinfo.Version) }
