package dotnet

// Outbound hosts from appsettings*.json. The files are JSON with
// comments and trailing commas, often with a BOM; a pre-pass blanks those out
// and keeps every newline, so lines still match the file.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/hostname"
)

// tolerantJSON drops a UTF-8 BOM and blanks comments and trailing commas
// outside strings with spaces; newlines stay.
func tolerantJSON(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	out := append([]byte(nil), b...)
	blank := func(i, j int) {
		for ; i < j; i++ {
			if out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
			}
		}
	}
	lastComma := -1 // a comma not yet followed by a value
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case c == '"':
			lastComma = -1
			for i++; i < len(out) && out[i] != '"' && out[i] != '\n'; i++ {
				if out[i] == '\\' {
					i++
				}
			}
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			j := bytes.IndexByte(out[i:], '\n')
			if j < 0 {
				j = len(out) - i
			}
			blank(i, i+j)
			i += j - 1
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			j := bytes.Index(out[i+2:], []byte("*/"))
			end := len(out)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			blank(i, end)
			i = end - 1
		case c == ',':
			lastComma = i
		case c == '}' || c == ']':
			if lastComma >= 0 {
				out[lastComma] = ' '
			}
			lastComma = -1
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
		default:
			lastComma = -1
		}
	}
	return out
}

func isAppSettings(f string) bool {
	b := strings.ToLower(path.Base(f))
	return strings.HasPrefix(b, "appsettings") && strings.HasSuffix(b, ".json")
}

// settingURL is the key convention for a URL the app calls: the spec's
// suffixes and the shipped config-name suffixes, any case.
func settingURL(key string) bool {
	return hasSuffixFold(key, "url", "uri", "endpoint", "host", "baseaddress") || defaults.Get().ConfigName(key)
}

// appSettings reads every tracked appsettings*.json outside test projects.
// URL values are default under a URL-shaped key, else mention; the
// ConnectionStrings section is left to the data-store readers and gives none.
func (s *state) appSettings(e *emitter) []fact.LocationError {
	var errs []fact.LocationError
	for _, f := range s.r.Files {
		if !isAppSettings(f) {
			continue
		}
		dir, inProject := s.projectDir(f)
		if inProject && (s.testDir(dir) || s.legacyDir(dir)) {
			continue
		}
		data, err := s.r.Read(f)
		if err != nil {
			errs = append(errs, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		err = walkJSON(tolerantJSON(data), func(keys []string, val string, line int) {
			if len(keys) == 0 || strings.EqualFold(keys[0], "ConnectionStrings") {
				return
			}
			h := hostname.FromURL(val)
			if h == "" {
				return
			}
			usage := "mention"
			if settingURL(keys[len(keys)-1]) {
				usage = "default"
			}
			e.host(h, usage, fact.Evidence{Path: f, Line: line, Unit: dir})
		})
		var le *lineError
		if errors.As(err, &le) {
			errs = append(errs, fact.LocationError{Path: f, Line: le.line, Message: "json: " + le.err.Error()})
		}
	}
	return errs
}

type lineError struct {
	line int
	err  error
}

func (e *lineError) Error() string { return e.err.Error() }

// walkJSON calls fn for every string value with the object keys above it
// (array elements take their array's key) and its line.
func walkJSON(data []byte, fn func(keys []string, val string, line int)) error {
	type frame struct {
		obj, wantKey bool
		key          string
	}
	var stack []frame
	dec := json.NewDecoder(bytes.NewReader(data))
	line, off := 1, 0
	lineAt := func(n int) int {
		if n > len(data) {
			n = len(data)
		}
		if n > off {
			line += bytes.Count(data[off:n], []byte("\n"))
			off = n
		}
		return line
	}
	keys := func() []string {
		var out []string
		for _, f := range stack {
			if f.obj {
				out = append(out, f.key)
			}
		}
		return out
	}
	afterValue := func() {
		if n := len(stack); n > 0 && stack[n-1].obj {
			stack[n-1].wantKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			if len(stack) > 0 {
				return &lineError{line: lineAt(len(data)), err: io.ErrUnexpectedEOF}
			}
			return nil
		}
		if err != nil {
			at := int(dec.InputOffset())
			var se *json.SyntaxError
			if errors.As(err, &se) {
				at = int(se.Offset)
			}
			return &lineError{line: lineAt(at), err: err}
		}
		l := lineAt(int(dec.InputOffset()))
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{obj: true, wantKey: true})
			case '[':
				stack = append(stack, frame{})
			default:
				stack = stack[:len(stack)-1]
				afterValue()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].obj && stack[n-1].wantKey {
				stack[n-1].key, stack[n-1].wantKey = v, false
				continue
			}
			fn(keys(), v, l)
			afterValue()
		default:
			afterValue()
		}
	}
}
