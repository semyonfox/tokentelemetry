package report

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

const maxGitMetadataBytes = 4096

// projectRef is the key used to aggregate one recorded CWD. It changes only
// when local Git metadata proves that two CWDs belong to the same checkout.
// Missing or malformed metadata therefore falls back to the recorded path
// instead of joining unrelated projects by guesswork.
type projectRef struct {
	key string
}

// projectCatalog resolves each distinct recorded CWD once per report and makes
// terminal labels unambiguous without using those labels as aggregate keys.
type projectCatalog struct {
	byRaw  map[string]projectRef
	labels map[string]string
	home   string
}

func newProjectCatalog(turns []model.Turn) *projectCatalog {
	c := &projectCatalog{
		byRaw:  make(map[string]projectRef),
		labels: make(map[string]string),
	}
	if home, err := os.UserHomeDir(); err == nil {
		c.home = normalizeProjectPath(home)
	}
	for _, t := range turns {
		c.ref(t.Project)
	}
	c.buildLabels()
	return c
}

func (c *projectCatalog) ref(raw string) projectRef {
	if ref, ok := c.byRaw[raw]; ok {
		return ref
	}

	ref := projectRef{}
	normalized := normalizeProjectPath(raw)
	if normalized == "" {
		ref.key = "(unknown)"
	} else if root, ok := discoverGitFamily(normalized); ok {
		ref.key = root
	} else {
		ref.key = normalized
	}
	c.byRaw[raw] = ref
	return ref
}

func (c *projectCatalog) label(key string) string {
	if label, ok := c.labels[key]; ok {
		return label
	}
	return projectBaseLabel(key, c.home)
}

func (c *projectCatalog) buildLabels() {
	byBase := make(map[string][]string)
	for _, ref := range c.byRaw {
		byBase[projectBaseLabel(ref.key, c.home)] = append(byBase[projectBaseLabel(ref.key, c.home)], ref.key)
	}
	for base, keys := range byBase {
		keys = uniqueSorted(keys)
		if len(keys) == 1 {
			c.labels[keys[0]] = base
			continue
		}

		maxDepth := 2
		for _, key := range keys {
			maxDepth = max(maxDepth, len(projectPathParts(key)))
		}
		remaining := keys
		for depth := 2; len(remaining) > 0 && depth <= maxDepth; depth++ {
			labels := make(map[string]int, len(keys))
			candidates := make(map[string]string, len(remaining))
			for _, key := range remaining {
				candidate := projectPathSuffix(key, depth)
				candidates[key] = candidate
				labels[candidate]++
			}
			next := make([]string, 0, len(remaining))
			for _, key := range remaining {
				if labels[candidates[key]] == 1 {
					c.labels[key] = candidates[key]
				} else {
					next = append(next, key)
				}
			}
			remaining = next
		}
		for _, key := range remaining {
			c.labels[key] = key
		}
	}
}

// normalizeProjectPath makes logically identical POSIX and Windows spellings
// compare equally without looking at the filesystem or resolving symlinks.
func normalizeProjectPath(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	p := strings.ReplaceAll(raw, `\`, "/")
	unc := strings.HasPrefix(p, "//")
	p = path.Clean(p)
	if unc && !strings.HasPrefix(p, "//") {
		p = "//" + strings.TrimPrefix(p, "/")
	}
	if isWindowsVolumePath(p) {
		p = strings.ToUpper(p[:1]) + p[1:]
		if len(p) == 2 {
			p += "/"
		}
	}
	return p
}

func isWindowsVolumePath(p string) bool {
	return len(p) >= 2 && p[1] == ':' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}

// discoverGitFamily returns the normal checkout root or, for a linked
// worktree, the main checkout root named by Git's commondir metadata.
func discoverGitFamily(normalized string) (string, bool) {
	local, ok := localProjectPath(normalized)
	if !ok {
		return "", false
	}
	info, err := os.Stat(local)
	if err != nil || !info.IsDir() {
		return "", false
	}

	for dir := local; ; {
		marker := filepath.Join(dir, ".git")
		if info, err := os.Stat(marker); err == nil {
			switch {
			case info.IsDir():
				return normalizeProjectPath(filepath.ToSlash(dir)), true
			case info.Mode().IsRegular():
				if mainRoot, ok := linkedWorktreeRoot(marker); ok {
					return normalizeProjectPath(filepath.ToSlash(mainRoot)), true
				}
				if validGitPointer(marker) {
					return normalizeProjectPath(filepath.ToSlash(dir)), true
				}
				return "", false
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func localProjectPath(normalized string) (string, bool) {
	// A Windows or UNC path recorded on a different host must not be probed as
	// a relative Linux path. It remains a lexical group instead.
	if filepath.Separator == '/' && (isWindowsVolumePath(normalized) || strings.HasPrefix(normalized, "//")) {
		return "", false
	}
	local := filepath.FromSlash(normalized)
	if !filepath.IsAbs(local) {
		return "", false
	}
	return local, true
}

func linkedWorktreeRoot(dotGit string) (string, bool) {
	gitDir, ok := gitDirFromFile(dotGit)
	if !ok {
		return "", false
	}

	commonDir, ok := readSmallFile(filepath.Join(gitDir, "commondir"))
	if !ok {
		return "", false
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return "", false
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	if filepath.Base(commonDir) != ".git" {
		return "", false
	}
	return filepath.Dir(commonDir), true
}

func validGitPointer(dotGit string) bool {
	gitDir, ok := gitDirFromFile(dotGit)
	if !ok {
		return false
	}
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}

func gitDirFromFile(dotGit string) (string, bool) {
	pointer, ok := readSmallFile(dotGit)
	if !ok {
		return "", false
	}
	const prefix = "gitdir:"
	pointer = strings.TrimSpace(pointer)
	if !strings.HasPrefix(pointer, prefix) {
		return "", false
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(pointer, prefix))
	if gitDir == "" {
		return "", false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(dotGit), gitDir)
	}
	return filepath.Clean(gitDir), true
}

func readSmallFile(name string) (string, bool) {
	f, err := os.Open(name)
	if err != nil {
		return "", false
	}
	defer f.Close()

	b, err := io.ReadAll(io.LimitReader(f, maxGitMetadataBytes+1))
	if err != nil || len(b) > maxGitMetadataBytes {
		return "", false
	}
	return string(b), true
}

func projectBaseLabel(key, home string) string {
	switch {
	case key == "(unknown)", key == "":
		return "(unknown)"
	case home != "" && key == home:
		return "~"
	case key == "/":
		return "/"
	case isWindowsVolumePath(key) && len(key) == 3 && key[2] == '/':
		return key
	}
	if base := path.Base(key); base != "." && base != "/" {
		return base
	}
	return key
}

func projectPathParts(key string) []string {
	if key == "(unknown)" || key == "/" {
		return []string{key}
	}
	parts := strings.Split(strings.Trim(key, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return []string{key}
	}
	return parts
}

func projectPathSuffix(key string, depth int) string {
	if key == "(unknown)" || key == "/" {
		return key
	}
	parts := projectPathParts(key)
	if len(parts) <= depth {
		return key
	}
	return strings.Join(parts[len(parts)-depth:], "/")
}

func uniqueSorted(items []string) []string {
	sort.Strings(items)
	out := items[:0]
	for _, item := range items {
		if len(out) == 0 || out[len(out)-1] != item {
			out = append(out, item)
		}
	}
	return out
}
