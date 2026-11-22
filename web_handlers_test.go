package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The web UI is plain scripts with inline handlers (onclick="doThing(...)"). A handler whose
// function is missing only fails when someone clicks it, so check every named handler exists.
// (A review fix once deleted two of them and the colour swatches stopped working.)
func TestInlineHandlersAreDefined(t *testing.T) {
	files, err := filepath.Glob("web/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("no web scripts found: %v", err)
	}
	var all strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteString("\n")
	}
	js := all.String()
	htmlSrc, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	pages := js + "\n" + string(htmlSrc)

	defined := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)\bfunction\s+([A-Za-z_$][\w$]*)\s*\(`),
		regexp.MustCompile(`(?m)^(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=`),
		regexp.MustCompile(`(?m)^window\.([A-Za-z_$][\w$]*)\s*=`),
	} {
		for _, m := range re.FindAllStringSubmatch(js, -1) {
			defined[m[1]] = true
		}
	}
	// names that are not ours: language keywords and browser objects used in short inline snippets
	builtin := map[string]bool{"if": true, "event": true, "this": true, "document": true, "window": true, "location": true,
		"alert": true, "confirm": true, "Math": true, "JSON": true, "history": true, "navigator": true, "return": true,
		"parseInt": true, "parseFloat": true, "Number": true, "String": true, "Boolean": true, "isNaN": true, "setTimeout": true, "clearTimeout": true}

	used := map[string]string{}
	add := func(name, from string) {
		if name != "" && !builtin[name] && !defined[name] {
			used[name] = from
		}
	}
	// onclick="name(", oninput="name(", onchange="name(" ... and `;name(` / `)name(` later in the same attribute
	attr := regexp.MustCompile(`\bon[a-z]+="([^"]*)"`)
	call := regexp.MustCompile(`(?:^|[;{(,\s=>!&|?:])([A-Za-z_$][\w$]*)\s*\(`)
	for _, m := range attr.FindAllStringSubmatch(pages, -1) {
		// drop quoted strings so text inside them is not mistaken for calls
		body := regexp.MustCompile(`'[^']*'`).ReplaceAllString(m[1], "''")
		for _, c := range call.FindAllStringSubmatch(body, -1) {
			if i := strings.Index(c[1], "."); i >= 0 {
				continue
			}
			add(c[1], m[0])
		}
	}
	// data-change="name" (switches) and a callback handed to segPick(id, value, callback)
	for _, m := range regexp.MustCompile(`data-change="([A-Za-z_$][\w$]*)"`).FindAllStringSubmatch(pages, -1) {
		add(m[1], m[0])
	}
	for _, m := range regexp.MustCompile(`(?:swrow|segField)\([^\n]*?,\s*'([A-Za-z_$][\w$]*)'\)`).FindAllStringSubmatch(pages, -1) {
		add(m[1], m[0])
	}
	for _, m := range regexp.MustCompile(`segPick\('[^']*','[^']*',\s*([A-Za-z_$][\w$]*)\)`).FindAllStringSubmatch(pages, -1) {
		add(m[1], m[0])
	}

	var names []string
	for n := range used {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Errorf("handler %q is used (%s) but no script defines it", n, used[n])
	}
}
