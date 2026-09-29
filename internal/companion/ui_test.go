package companion

import (
	"regexp"
	"strings"
	"testing"
)

// The dashboard builds rows from JavaScript strings, so a CSS rule can silently
// stop matching anything: .clipboard-row styled the clipboard line for a while
// but no code ever applied the class, leaving the item count glued to its name.
// Every "*-row" class the stylesheet declares must therefore be used by the page.
func TestDashboardStylesheetRowClassesAreUsedByThePage(t *testing.T) {
	page, err := dashboardFiles.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := dashboardFiles.ReadFile("ui/assets/styles.css")
	if err != nil {
		t.Fatal(err)
	}

	declared := map[string]bool{}
	for _, match := range regexp.MustCompile(`\.([a-z][a-z0-9]*(?:-[a-z0-9]+)*-row)\b`).FindAllStringSubmatch(string(styles), -1) {
		declared[match[1]] = true
	}
	if len(declared) == 0 {
		t.Fatal("expected the stylesheet to declare row classes")
	}
	for class := range declared {
		if !strings.Contains(string(page), class) {
			t.Errorf("stylesheet styles .%s but index.html never applies it", class)
		}
	}
}
