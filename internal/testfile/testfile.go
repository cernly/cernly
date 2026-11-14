// Package testfile tells test code from other code by each language's own
// convention, from the path alone (size, test_suite reuses it). Nothing is
// parsed or run. See https://cernly.dev/how/plugins/.
package testfile

import (
	"path"
	"strings"
)

// dirs hold test code in every language that uses them.
var dirs = map[string]bool{"test": true, "tests": true, "__tests__": true, "spec": true, "testdata": true, "e2e": true}

// testSourceSets are the src/ folders Maven and Gradle compile as tests.
var testSourceSets = map[string]bool{"test": true, "testFixtures": true, "integrationTest": true}

// languages maps the extension of test code to its language, as test_suite
// facts name it: TypeScript is javascript, Kotlin is java.
var languages = map[string]string{
	".go": "go",
	".ts": "javascript", ".tsx": "javascript", ".js": "javascript", ".jsx": "javascript", ".mjs": "javascript",
	".cjs": "javascript", ".mts": "javascript", ".cts": "javascript", ".vue": "javascript", ".svelte": "javascript",
	".py": "python", ".java": "java", ".kt": "java", ".groovy": "java", ".scala": "java", ".rb": "ruby",
	".cs": "csharp", ".fs": "fsharp", ".vb": "vb",
}

// dotnetDirs end the folder name of a .NET test project by convention
// (Shop.Tests, Shop.UnitTests), compared without regard to case.
var dotnetDirs = []string{".tests", ".unittests", ".integrationtests", ".specs"}

// Language returns the language of a test file: Is, a source extension,
// and not under testdata (fixtures a test reads are not tests). "" otherwise.
func Language(p string) string {
	lang := languages[strings.ToLower(path.Ext(p))]
	if lang == "" || !Is(p) {
		return ""
	}
	for _, d := range strings.Split(path.Dir(p), "/") {
		if d == "testdata" {
			return ""
		}
	}
	return lang
}

// Is reports whether the repo path is test code by convention:
//
//	go:          *_test.go
//	ts/js:       *.test.*, *.spec.*, __tests__/
//	python:      test_*.py, *_test.py, conftest.py, tests/
//	java/kotlin: src/test/ (and Gradle's src/testFixtures/, src/integrationTest/),
//	             *Test.java, *Tests.java, *IT.java (and .kt, .groovy)
//	ruby:        *_spec.rb, *_test.rb, spec/
//	.NET:        *Test.cs, *Tests.cs (and .fs, .vb), a folder ending in .Tests,
//	             .UnitTests, .IntegrationTests or .Specs in any case; a path stand-in for
//	             the files of a test project, which the dotnet plugin reads
//	any:         a folder named test, tests, spec, e2e or testdata
func Is(p string) bool {
	b := path.Base(p)
	lower := strings.ToLower(b)
	ext := path.Ext(lower)
	stem := strings.TrimSuffix(b, path.Ext(b))
	switch ext {
	case ".go":
		if strings.HasSuffix(lower, "_test.go") {
			return true
		}
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte":
		s := strings.TrimSuffix(lower, ext)
		if strings.HasSuffix(s, ".test") || strings.HasSuffix(s, ".spec") {
			return true
		}
	case ".py":
		if strings.HasPrefix(lower, "test_") || strings.HasSuffix(lower, "_test.py") || lower == "conftest.py" {
			return true
		}
	case ".java", ".kt", ".kts", ".scala", ".groovy":
		if strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "Tests") || strings.HasSuffix(stem, "IT") {
			return true
		}
	case ".rb":
		if strings.HasSuffix(lower, "_spec.rb") || strings.HasSuffix(lower, "_test.rb") {
			return true
		}
	case ".cs", ".fs", ".vb":
		if strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "Tests") {
			return true
		}
		for _, d := range strings.Split(path.Dir(p), "/") {
			for _, s := range dotnetDirs {
				if strings.HasSuffix(strings.ToLower(d), s) {
					return true
				}
			}
		}
	}
	parts := strings.Split(p, "/")
	for i, d := range parts[:len(parts)-1] {
		if dirs[strings.ToLower(d)] {
			return true
		}
		if d == "src" && i+1 < len(parts)-1 && testSourceSets[parts[i+1]] {
			return true
		}
	}
	return false
}
