package web

import (
	"strings"
	"testing"
)

func TestNewProviderIconsEmbedded(t *testing.T) {
	for _, provider := range []string{"chutes", "googleaistudio", "nvidia"} {
		icon, err := staticFS.ReadFile("static/icons/" + provider + ".svg")
		if err != nil || !strings.Contains(string(icon), "<svg") {
			t.Fatalf("%s provider icon not embedded: %v", provider, err)
		}
	}
}

func TestNewProviderMenubarIconsWired(t *testing.T) {
	page, err := staticFS.ReadFile("static/menubar.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, provider := range []string{"chutes", "googleaistudio", "nvidia"} {
		for _, needle := range []string{
			`.provider-icon-` + provider + ` {`,
			`/static/icons/` + provider + `.svg`,
			`case "` + provider + `":`,
			`return "provider-icon-` + provider + `";`,
		} {
			if !strings.Contains(html, needle) {
				t.Errorf("menubar.html is missing %q", needle)
			}
		}
	}
}
