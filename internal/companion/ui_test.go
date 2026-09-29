package companion

import (
	"bytes"
	"os"
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

const designSystemAssets = "../../design-systems/agentclip-companion/assets/"

// The design system page is the reference the dashboard was built from. It only
// shows the static components, so the dashboard has more rules (offline states,
// inbound offers) than it does, but every rule it does show must be the very
// same in the dashboard. Otherwise a tweak made in one place, such as the page
// padding, silently leaves the other behind.
func TestDesignSystemStylesAreASubsetOfTheDashboardStyles(t *testing.T) {
	design, err := os.ReadFile(designSystemAssets + "styles.css")
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := dashboardFiles.ReadFile("ui/assets/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	inDashboard := map[string]bool{}
	for _, line := range strings.Split(string(dashboard), "\n") {
		inDashboard[strings.TrimSpace(line)] = true
	}
	for number, line := range strings.Split(string(design), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !inDashboard[line] {
			t.Errorf("design-systems styles.css line %d is not in the dashboard stylesheet (change both together):\n\t%s", number+1, line)
		}
	}
}

func TestDesignSystemBrandAssetsMatchTheDashboard(t *testing.T) {
	for _, name := range []string{"agentclip-mark.svg", "favicon.svg"} {
		design, err := os.ReadFile(designSystemAssets + name)
		if err != nil {
			t.Fatal(err)
		}
		dashboard, err := dashboardFiles.ReadFile("ui/assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(design, dashboard) {
			t.Errorf("%s differs between the design system and the dashboard", name)
		}
	}
}
