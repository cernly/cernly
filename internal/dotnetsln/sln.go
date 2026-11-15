// Package dotnetsln reads the project list of a .sln or .slnx file.
package dotnetsln

import (
	"encoding/xml"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/cernly/cernly/repo"
)

// slnProject matches a project line of a .sln file.
var slnProject = regexp.MustCompile(`^\s*Project\("[^"]*"\)\s*=\s*"[^"]*"\s*,\s*"([^"]+)"`)

// Projects returns the sorted folders of the projects a solution file
// lists. Paths are relative to the solution's folder; a solution folder (no
// project file extension) and a path that leaves the repo are skipped.
func Projects(data []byte, slnPath string) ([]string, error) {
	var raw []string
	if strings.EqualFold(path.Ext(slnPath), ".slnx") {
		dec := xml.NewDecoder(strings.NewReader(string(data)))
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Project" {
				for _, a := range se.Attr {
					if a.Name.Local == "Path" {
						raw = append(raw, a.Value)
					}
				}
			}
		}
	} else {
		for _, line := range strings.Split(string(data), "\n") {
			if m := slnProject.FindStringSubmatch(line); m != nil {
				raw = append(raw, m[1])
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range raw {
		p = strings.ReplaceAll(p, `\`, "/")
		if !strings.HasSuffix(strings.ToLower(path.Ext(p)), "proj") {
			continue
		}
		d, ok := repo.Join(path.Dir(slnPath), path.Dir(p))
		if !ok || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}
