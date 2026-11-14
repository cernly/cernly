package testfile

import "testing"

func TestLanguage(t *testing.T) {
	for p, want := range map[string]string{
		"api/store/store_test.go":                   "go",
		"web/src/app.test.tsx":                      "javascript",
		"svc/tests/test_x.py":                       "python",
		"app/src/test/kotlin/x/FooSpec.kt":          "java",
		"app/src/integrationTest/java/x/Db.java":    "java",
		"app/src/test/groovy/x/FooSpec.groovy":      "java",
		"app/src/test/resources/data.json":          "",
		"core/cli/testdata/fixture/backend/main.go": "",
		"api/store/store.go":                        "",
	} {
		if got := Language(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
}

func TestIs(t *testing.T) {
	for p, want := range map[string]bool{
		"api/store/store_test.go":                   true,
		"api/store/store.go":                        false,
		"web/src/app.test.tsx":                      true,
		"web/src/app.spec.ts":                       true,
		"web/src/__tests__/x.js":                    true,
		"web/src/contest.ts":                        false,
		"svc/test_models.py":                        true,
		"svc/models_test.py":                        true,
		"svc/conftest.py":                           true,
		"svc/tests/helpers.py":                      true,
		"svc/latest.py":                             false,
		"app/src/test/java/x/FooService.java":       true,
		"app/src/main/java/x/FooServiceTest.java":   true,
		"app/src/main/java/x/FooIT.java":            true,
		"app/src/main/java/x/Contest.java":          false,
		"lib/foo_spec.rb":                           true,
		"e2e/login.ts":                              true,
		"internal/attest/attest.go":                 false,
		"core/cli/testdata/fixture/backend/main.go": true,
	} {
		if got := Is(p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}

// The .NET convention is a path stand-in for "the files of a test project":
// a folder named like a test project in any case, or a *Test or
// *Tests file.
func TestTestfileCSharp(t *testing.T) {
	for p, want := range map[string]string{
		"src/Shop.Tests/OrderTests.cs":      "csharp",
		"src/Shop.UnitTests/X.cs":           "csharp",
		"src/Shop.IntegrationTests/Db/X.cs": "csharp",
		"src/Shop.Specs/X.cs":               "csharp",
		"src/Shop/OrderTests.cs":            "csharp",
		"src/Shop.Tests/Tests.fs":           "fsharp",
		"src/Shop.Tests/X.vb":               "vb",
		"src/Shop/Order.cs":                 "",
		"src/Shop/Contests.cs":              "",
		"src/Shop/OrderTest.cs":             "csharp",
		"src/Shop/OrderTest.vb":             "vb",
		"src/Shop/Latest.cs":                "",
		"src/shop.tests/X.cs":               "csharp",
		"src/Shop.UNITTESTS/X.fs":           "fsharp",
		"src/Shop.integrationtests/X.cs":    "csharp",
		"src/Shop.specs/X.cs":               "csharp",
		"src/Shop/Order.fs":                 "",
		"src/Shop.Tests.Helpers/X.cs":       "",
		"src/Shop.Tests/testdata/Sample.cs": "",
	} {
		if got := Language(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
	if !Is("src/Shop.Tests/OrderTests.cs") || !Is("src/Shop.UnitTests/X.cs") || Is("src/Shop/Order.cs") {
		t.Error("Is disagrees with the .NET convention")
	}
}
