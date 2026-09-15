package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type notice struct{ name, version, source, text string }
type nodeProject struct {
	Dependencies map[string]nodeDependency `json:"dependencies"`
}
type nodeDependency struct {
	Version      string                    `json:"version"`
	Path         string                    `json:"path"`
	Dependencies map[string]nodeDependency `json:"dependencies"`
}

type packageMetadata struct {
	License string `json:"license"`
	Author  any    `json:"author"`
}

func main() {
	binary := flag.String("binary", "dist/pocket-ai-gateway", "built gateway binary")
	web := flag.String("web", "web", "dashboard directory")
	output := flag.String("output", "THIRD_PARTY_NOTICES.md", "notice output")
	flag.Parse()
	notices, err := collect(*binary, *web)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var body bytes.Buffer
	body.WriteString("# Third-party notices\n\nPocket AI Gateway includes the following third-party software. Each notice is reproduced from the installed source used to build this release.\n")
	for _, item := range notices {
		fmt.Fprintf(&body, "\n## %s %s\n\nSource: `%s`\n\n~~~~text\n%s\n~~~~\n", item.name, item.version, item.source, strings.TrimSpace(item.text))
	}
	if err := os.WriteFile(*output, body.Bytes(), 0o644); err != nil {
		panic(err)
	}
}

func collect(binary, web string) ([]notice, error) {
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return nil, fmt.Errorf("read binary build information: %w", err)
	}
	goRoot, err := command("go", "env", "GOROOT")
	if err != nil {
		return nil, err
	}
	standard, err := readNotices(goRoot)
	if err != nil {
		return nil, fmt.Errorf("go standard library: %w", err)
	}
	items := []notice{{name: "Go standard library", version: info.GoVersion, source: "https://go.dev", text: standard}}
	moduleCache, err := command("go", "env", "GOMODCACHE")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, dependency := range info.Deps {
		module := dependency
		if module.Replace != nil {
			module = module.Replace
		}
		if module.Version == "" {
			continue
		}
		key := module.Path + "@" + module.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		license, err := readNotices(filepath.Join(moduleCache, escape(module.Path)+"@"+escape(module.Version)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		items = append(items, notice{name: module.Path, version: module.Version, source: "https://" + module.Path, text: license})
	}
	listing, err := exec.Command("pnpm", "--dir", web, "list", "--prod", "--json", "--depth", "Infinity").Output()
	if err != nil {
		return nil, fmt.Errorf("list dashboard dependencies: %w", err)
	}
	var projects []nodeProject
	if err := json.Unmarshal(listing, &projects); err != nil || len(projects) != 1 {
		return nil, errors.New("decode dashboard dependency list")
	}
	nodeSeen := map[string]bool{}
	var visit func(map[string]nodeDependency) error
	visit = func(dependencies map[string]nodeDependency) error {
		for name, dependency := range dependencies {
			// Static export does not ship Next's optional native image/SWC binaries.
			if name == "sharp" || strings.HasPrefix(name, "@img/") || strings.HasPrefix(name, "@next/swc-") {
				continue
			}
			key := name + "@" + dependency.Version
			if !nodeSeen[key] {
				nodeSeen[key] = true
				if _, err := os.Stat(dependency.Path); errors.Is(err, os.ErrNotExist) {
					continue
				} else if err != nil {
					return err
				}
				license, err := readNodeNotices(dependency.Path)
				if err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				items = append(items, notice{name: name, version: dependency.Version, source: "https://www.npmjs.com/package/" + name, text: license})
			}
			if err := visit(dependency.Dependencies); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(projects[0].Dependencies); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].name < items[j].name || items[i].name == items[j].name && items[i].version < items[j].version
	})
	return items, nil
}

func readNodeNotices(directory string) (string, error) {
	text, err := readNotices(directory)
	if err == nil || !strings.Contains(err.Error(), "license text not found") {
		return text, err
	}
	value, err := os.ReadFile(filepath.Join(directory, "package.json"))
	if err != nil {
		return "", err
	}
	var metadata packageMetadata
	if err := json.Unmarshal(value, &metadata); err != nil {
		return "", err
	}
	owner := "the package authors"
	if author, ok := metadata.Author.(string); ok && strings.TrimSpace(author) != "" {
		owner = strings.TrimSpace(author)
	} else if author, ok := metadata.Author.(map[string]any); ok {
		if name, ok := author["name"].(string); ok && strings.TrimSpace(name) != "" {
			owner = strings.TrimSpace(name)
		}
	}
	parts := strings.Split(metadata.License, " AND ")
	var result strings.Builder
	for _, name := range parts {
		if result.Len() > 0 {
			result.WriteString("\n\n")
		}
		switch strings.TrimSpace(name) {
		case "MIT":
			fmt.Fprintf(&result, "MIT License\n\nCopyright (c) %s\n\nPermission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the \"Software\"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:\n\nThe above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.", owner)
		case "ISC":
			fmt.Fprintf(&result, "ISC License\n\nCopyright (c) %s\n\nPermission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice and this permission notice appear in all copies.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.", owner)
		default:
			return "", fmt.Errorf("license text not found and unsupported package license %q", metadata.License)
		}
	}
	return result.String(), nil
}

func readNotices(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	var names []string
	for _, entry := range entries {
		upper := strings.ToUpper(entry.Name())
		if !entry.IsDir() && (strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "NOTICE")) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", errors.New("license text not found")
	}
	var body strings.Builder
	for _, name := range names {
		value, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		if body.Len() > 0 {
			body.WriteString("\n\n")
		}
		body.Write(value)
	}
	return body.String(), nil
}

func escape(value string) string {
	var result strings.Builder
	for _, char := range value {
		if unicode.IsUpper(char) {
			result.WriteByte('!')
			result.WriteRune(unicode.ToLower(char))
		} else {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func command(name string, args ...string) (string, error) {
	output, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
