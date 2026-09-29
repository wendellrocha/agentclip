package docs

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const root = "../../"

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(root + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// codeLines returns the commands in the fenced blocks of a README, without
// comments, which are translated.
func codeLines(markdown string) []string {
	var lines []string
	inside := false
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "```") {
			inside = !inside
			continue
		}
		trimmed := strings.TrimSpace(line)
		if inside && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// The English README is a translation, so its commands must be exactly the
// Portuguese ones: a command changed in one and not the other would give
// readers of one language a broken instruction.
func TestEnglishReadmeHasTheSameCommandsAsThePortugueseOne(t *testing.T) {
	portuguese := codeLines(read(t, "README.md"))
	english := codeLines(read(t, "README.en.md"))
	if len(portuguese) == 0 {
		t.Fatal("found no commands in README.md")
	}
	longest := len(portuguese)
	if len(english) > longest {
		longest = len(english)
	}
	for i := 0; i < longest; i++ {
		var pt, en string
		if i < len(portuguese) {
			pt = portuguese[i]
		}
		if i < len(english) {
			en = english[i]
		}
		if pt != en {
			t.Fatalf("command %d differs between the READMEs:\n  README.md:    %q\n  README.en.md: %q", i+1, pt, en)
		}
	}
}

func TestEnglishReadmeHasTheSameSectionsAsThePortugueseOne(t *testing.T) {
	headings := regexp.MustCompile(`(?m)^#{2,3} `)
	pt := len(headings.FindAllString(read(t, "README.md"), -1))
	en := len(headings.FindAllString(read(t, "README.en.md"), -1))
	if pt == 0 || pt != en {
		t.Fatalf("README.md has %d sections and README.en.md has %d; translate every section", pt, en)
	}
}

func TestTheReadmesLinkToEachOther(t *testing.T) {
	if !strings.Contains(read(t, "README.md"), "(README.en.md)") {
		t.Error("README.md does not link to the English README")
	}
	if !strings.Contains(read(t, "README.en.md"), "(README.md)") {
		t.Error("README.en.md does not link to the Portuguese README")
	}
}

// Both READMEs describe the same environment variable and the same first
// release with an attestation, so they must name the same values.
func TestReadmesAgreeOnTheAttestationSettings(t *testing.T) {
	// Every mention must be the same version, so one that drifts is caught.
	// SECURITY.md holds both languages.
	for name, want := range map[string]int{"README.md": 2, "README.en.md": 2, "SECURITY.md": 2} {
		text := read(t, name)
		for _, version := range regexp.MustCompile(`v\d+\.\d+\.\d+-rc\.\d+`).FindAllString(text, -1) {
			if version != "v0.7.1-rc.1" {
				t.Errorf("%s names %s as a release with an attestation, want v0.7.1-rc.1", name, version)
			}
		}
		if got := strings.Count(text, "v0.7.1-rc.1"); got != want {
			t.Errorf("%s mentions the first release with an attestation %d time(s), want %d", name, got, want)
		}
	}
	for _, name := range []string{"README.md", "README.en.md"} {
		if !strings.Contains(read(t, name), "AGENTCLIP_SKIP_ATTESTATION=1") {
			t.Errorf("%s does not document AGENTCLIP_SKIP_ATTESTATION=1", name)
		}
	}
}
