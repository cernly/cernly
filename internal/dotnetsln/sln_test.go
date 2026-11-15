package dotnetsln

import (
	"reflect"
	"testing"
)

func TestProjectsSln(t *testing.T) {
	data := []byte("Microsoft Visual Studio Solution File, Format Version 12.00\r\n" +
		"Project(\"{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}\") = \"Shop\", \"src\\Shop\\Shop.csproj\", \"{A1}\"\r\nEndProject\r\n" +
		"Project(\"{2150E333-8FDC-42A3-9474-1A3956D46DE8}\") = \"tests\", \"tests\", \"{B2}\"\r\nEndProject\r\n" +
		"Project(\"{F2A71F9B-5D33-465A-A702-920D77279786}\") = \"Shop.Tests\", \"..\\tests\\Shop.Tests\\Shop.Tests.fsproj\", \"{C3}\"\r\nEndProject\r\n" +
		"Project(\"{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}\") = \"Outside\", \"..\\..\\Other\\Other.csproj\", \"{D4}\"\r\nEndProject\r\n")
	got, err := Projects(data, "build/All.sln")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build/src/Shop", "tests/Shop.Tests"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestProjectsSlnx(t *testing.T) {
	data := []byte(`<Solution>
  <Folder Name="/tests/">
    <Project Path="tests\Shop.Tests\Shop.Tests.csproj" />
    <Project Path="../escape/X.csproj" />
  </Folder>
  <Folder Name="/Solution Items/"><File Path="README.md" /></Folder>
  <Project Path="src/Shop/Shop.csproj" Id="x"/>
  <Project Path="docs" />
</Solution>`)
	got, err := Projects(data, "Shop.slnx")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/Shop", "tests/Shop.Tests"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := Projects([]byte("<Solution><Project"), "Shop.slnx"); err == nil {
		t.Error("broken slnx should be an error")
	}
}
