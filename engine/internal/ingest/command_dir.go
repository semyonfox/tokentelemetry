package ingest

import (
	"cmp"
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// commandDirPatterns find where a tool call explicitly worked: a cd, a tool's
// own directory flag, or a patched file. Plain path mentions do not count,
// because reading a file elsewhere is not working there. Only absolute and
// home-relative paths qualify, since a relative one already lives under the
// session's cwd.
var commandDirPatterns = []struct {
	re   *regexp.Regexp
	file bool
}{
	{re: regexp.MustCompile("(?:^|[\\s;&|(\"'`])cd\\s+[\"']?((?:/|~/)[^\\s\"';&|)`\\\\]*)")},
	{re: regexp.MustCompile("\\bgit\\s+-C\\s+[\"']?((?:/|~/)[^\\s\"';&|)`\\\\]*)")},
	{re: regexp.MustCompile("\\b(?:make|pnpm|npm)\\s+(?:-C|--dir|--prefix)\\s+[\"']?((?:/|~/)[^\\s\"';&|)`\\\\]*)")},
	{re: regexp.MustCompile(`\*\*\* (?:Add|Update|Delete) File:\s*((?:/|~/)[^\s\\]+)`), file: true},
}

// workdirPattern finds a shell's starting directory written inline, as Codex's
// code mode does in JavaScript. A cd inside the command moves on from it, so it
// only counts when the command names no location of its own.
var workdirPattern = regexp.MustCompile(`(?:"workdir"|\bworkdir)\s*:\s*["']((?:/|~/)[^"'\n\\]*)["']`)

// shellArgs is a shell tool's JSON arguments. Codex writes the command as a
// string or as an argv list.
type shellArgs struct {
	Cmd     json.RawMessage `json:"cmd"`
	Command json.RawMessage `json:"command"`
	Workdir string          `json:"workdir"`
}

// commandDir returns the last directory a tool call's input explicitly worked
// in, or "" when it names none. home expands a leading ~/.
func commandDir(input, home string) string {
	var args shellArgs
	if strings.HasPrefix(strings.TrimSpace(input), "{") && json.Unmarshal([]byte(input), &args) == nil {
		// decoded, so quoted paths inside the command are no longer escaped
		if dir := cmp.Or(commandLocation(args.Cmd, home), commandLocation(args.Command, home)); dir != "" {
			return dir
		}
		if strings.HasPrefix(args.Workdir, "/") || strings.HasPrefix(args.Workdir, "~/") {
			return expandHomeDir(args.Workdir, home)
		}
		return ""
	}
	if dir := locationIn(input, home); dir != "" {
		return dir
	}
	if m := workdirPattern.FindAllStringSubmatch(input, -1); len(m) > 0 {
		return expandHomeDir(m[len(m)-1][1], home)
	}
	return ""
}

// commandLocation reads a command given as one shell string or as an argv
// list. Each argv entry is read on its own, so a path with spaces stays whole.
func commandLocation(raw json.RawMessage, home string) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return locationIn(text, home)
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil {
		return ""
	}
	dir := ""
	for i, arg := range argv {
		if d := locationIn(arg, home); d != "" {
			dir = d
		}
		if i > 0 && (strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~/")) {
			switch argv[i-1] {
			case "cd", "-C", "--dir", "--prefix":
				dir = expandHomeDir(arg, home)
			}
		}
	}
	return dir
}

// locationIn returns the last location a command names, by position.
func locationIn(text, home string) string {
	best, bestAt := "", -1
	for _, p := range commandDirPatterns {
		for _, m := range p.re.FindAllStringSubmatchIndex(text, -1) {
			if m[2] < bestAt {
				continue
			}
			dir := text[m[2]:m[3]]
			if p.file {
				dir = path.Dir(dir)
			}
			best, bestAt = dir, m[2]
		}
	}
	return expandHomeDir(best, home)
}

// fileDir is the directory of a file an edit tool wrote.
func fileDir(file, home string) string {
	if !strings.HasPrefix(file, "/") && !strings.HasPrefix(file, "~/") {
		return ""
	}
	return expandHomeDir(path.Dir(file), home)
}

func expandHomeDir(dir, home string) string {
	if strings.HasPrefix(dir, "~/") {
		if home == "" {
			return ""
		}
		return path.Join(home, dir[2:])
	}
	return dir
}
