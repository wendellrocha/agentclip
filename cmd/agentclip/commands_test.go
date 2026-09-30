package main

import (
	"strings"
	"testing"
)

// The table is the one place that says what commands exist, so it has to be
// consistent: help and dispatch both read it.
func TestCommandTableIsConsistent(t *testing.T) {
	seen := map[string]string{}
	for _, c := range commandTable() {
		for _, name := range append([]string{c.name}, c.aliases...) {
			if other, dup := seen[name]; dup {
				t.Errorf("%q is used by both %s and %s", name, other, c.name)
			}
			seen[name] = c.name
		}
		if c.run == nil || strings.TrimSpace(c.summary) == "" || strings.TrimSpace(c.details) == "" || len(c.usage) == 0 {
			t.Errorf("command %q is missing its handler, summary, details or usage", c.name)
		}
		named := false
		for _, line := range c.usage {
			if !strings.HasPrefix(line, "agentclip ") {
				t.Errorf("usage %q of %q does not start with agentclip", line, c.name)
			}
			named = named || strings.HasPrefix(line, "agentclip "+c.name)
		}
		if !named {
			t.Errorf("no usage line of %q starts with its own name", c.name)
		}
		known := false
		for _, group := range groupOrder {
			known = known || c.group == group
		}
		if !known {
			t.Errorf("command %q is in the unknown group %q", c.name, c.group)
		}
	}
	// Everything that worked before the table still does.
	for _, name := range []string{"arm", "ssh", "pair", "setup", "connect", "uninstall", "disconnect", "companion", "mcp", "harness", "bridge", "doctor", "logs", "upgrade", "help", "-h", "--help", "version", "--version", "-v"} {
		if _, ok := findCommand(commandTable(), name); !ok {
			t.Errorf("the command %q is gone", name)
		}
	}
}

func TestHelpListsEveryCommandOnceUnderItsGroup(t *testing.T) {
	var out strings.Builder
	table := commandTable()
	printHelp(&out, table)
	for _, c := range table {
		if strings.Count(out.String(), "\n  "+c.name+" ") != 1 {
			t.Errorf("help lists %q %d times:\n%s", c.name, strings.Count(out.String(), "\n  "+c.name+" "), out.String())
		}
	}
	for _, group := range groupOrder {
		if !strings.Contains(out.String(), groupTitle(group)+":") {
			t.Errorf("help lacks the group %q", group)
		}
	}
}
