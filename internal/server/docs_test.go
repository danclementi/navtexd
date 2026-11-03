package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"navtex/internal/config"
)

// Every option name that the help pages write as <code class="opt">{input.hpsdr.mirror_iq}</code> must exist (names with a *
// stand for any word or, at the end, for a whole group). The Options tab shows the same names in braces.
func TestHelpOptionNamesExist(t *testing.T) {
	var paths []string
	for _, f := range config.Fields(config.Default()) {
		p := f.Path
		if i := strings.Index(p, "."); strings.HasPrefix(p, "extra.") && i >= 0 { // extra.0.channel.name -> channel.name
			p = p[strings.Index(p[i+1:], ".")+i+2:]
		}
		paths = append(paths, p)
	}
	re := regexp.MustCompile(`<code class="opt">\{([^}]*)\}</code>`)
	files, _ := filepath.Glob(filepath.Join("web", "docs", "*.html"))
	if len(files) == 0 {
		t.Fatal("no help pages found")
	}
	n := 0
	for _, fn := range files {
		b, err := os.ReadFile(fn)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			name := m[1]
			n++
			pat := "^" + strings.NewReplacer(".", `\.`, "*", `[^.]+`).Replace(name) + "$"
			if strings.HasSuffix(name, ".*") { // a whole group
				pat = "^" + strings.NewReplacer(".", `\.`, "*", `[^.]+`).Replace(strings.TrimSuffix(name, ".*")) + `\..+$`
			}
			rx := regexp.MustCompile(pat)
			found := false
			for _, p := range paths {
				if rx.MatchString(p) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: option {%s} does not exist", filepath.Base(fn), name)
			}
		}
	}
	if n < 50 {
		t.Errorf("only %d option names found in the help pages", n)
	}
}
