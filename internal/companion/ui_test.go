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
	dashboardRules := parseCSSDeclarations(string(dashboard))
	for key, declarations := range parseCSSDeclarations(string(design)) {
		for declaration := range declarations {
			if !dashboardRules[key][declaration] {
				t.Errorf("design-systems styles.css has %q in %s, but the dashboard stylesheet does not (change both together)", declaration, key)
			}
		}
	}
}

// parseCSSDeclarations maps "selector" (or "@media ... > selector") to the set
// of declarations written under it, so a declaration moved to another rule is a
// difference even though the line itself still exists somewhere.
func parseCSSDeclarations(css string) map[string]map[string]bool {
	rules := map[string]map[string]bool{}
	var walk func(context string, text string)
	walk = func(context string, text string) {
		for {
			open := strings.Index(text, "{")
			if open < 0 {
				return
			}
			depth, end := 1, open+1
			for ; end < len(text) && depth > 0; end++ {
				switch text[end] {
				case '{':
					depth++
				case '}':
					depth--
				}
			}
			selector := strings.Join(strings.Fields(text[:open]), " ")
			body := text[open+1 : end-1]
			text = text[end:]
			if strings.HasPrefix(selector, "@media") {
				walk(context+selector+" > ", body)
				continue
			}
			key := context + selector
			if rules[key] == nil {
				rules[key] = map[string]bool{}
			}
			for _, declaration := range strings.Split(body, ";") {
				if declaration = strings.Join(strings.Fields(declaration), " "); declaration != "" {
					rules[key][declaration] = true
				}
			}
		}
	}
	walk("", css)
	return rules
}

// The parser behind the comparison must tell a moved declaration from an
// unchanged one, or the parity test above proves nothing.
func TestParseCSSDeclarationsKeepsRulesAndMediaContextsApart(t *testing.T) {
	rules := parseCSSDeclarations(":root { --canvas: #fff; }\nbody { color: red; }\n@media (max-width: 1px) { body { color: blue; } }")
	if !rules[":root"]["--canvas: #fff"] || rules["body"]["--canvas: #fff"] {
		t.Fatalf("declaration attributed to the wrong rule: %v", rules)
	}
	if !rules["body"]["color: red"] || !rules["@media (max-width: 1px) > body"]["color: blue"] || rules["body"]["color: blue"] {
		t.Fatalf("media context not kept apart: %v", rules)
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
