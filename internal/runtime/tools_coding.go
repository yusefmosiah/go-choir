package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func RegisterFileTools(registry *ToolRegistry, cwd string) error {
	for _, tool := range []Tool{
		newReadFileTool(cwd),
		newWriteFileTool(cwd),
		newEditFileTool(cwd),
		newGlobTool(cwd),
		newGrepTool(cwd),
	} {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func RegisterReadOnlyFileTools(registry *ToolRegistry, cwd string) error {
	for _, tool := range []Tool{
		newReadFileTool(cwd),
		newGlobTool(cwd),
		newGrepTool(cwd),
	} {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func RegisterCodingTools(registry *ToolRegistry, cwd string) error {
	for _, tool := range []Tool{
		newBashTool(cwd),
		newGitStatusTool(cwd),
		newGitDiffTool(cwd),
	} {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func newReadFileTool(cwd string) Tool {
	type args struct {
		Path string `json:"path"`
	}
	return Tool{
		Name:        "read_file",
		Description: "Read a file from disk relative to the sandbox working directory.",
		Parameters: jsonSchemaObject(map[string]any{
			"path": map[string]any{"type": "string"},
		}, []string{"path"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode read_file args: %w", err)
			}
			resolved, err := resolveToolPath(cwd, in.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", err
			}
			content := string(data)
			if len(content) > 100*1024 {
				content = content[:100*1024] + fmt.Sprintf("\n\n[file truncated — %d bytes total, showing first 100KB]", len(data))
			}
			return toolResultJSON(map[string]any{
				"path":    resolved,
				"content": content,
			})
		},
	}
}

func newWriteFileTool(cwd string) Tool {
	type args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	return Tool{
		Name:        "write_file",
		Description: "Create or overwrite a file relative to the sandbox working directory.",
		Parameters: jsonSchemaObject(map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		}, []string{"path", "content"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode write_file args: %w", err)
			}
			resolved, err := resolveToolPath(cwd, in.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(resolved, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"path":          resolved,
				"bytes_written": len(in.Content),
			})
		},
	}
}

func newEditFileTool(cwd string) Tool {
	type args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all,omitempty"`
	}
	return Tool{
		Name:        "edit_file",
		Description: "Replace text in an existing file.",
		Parameters: jsonSchemaObject(map[string]any{
			"path":        map[string]any{"type": "string"},
			"old_string":  map[string]any{"type": "string"},
			"new_string":  map[string]any{"type": "string"},
			"replace_all": map[string]any{"type": "boolean"},
		}, []string{"path", "old_string", "new_string"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode edit_file args: %w", err)
			}
			if strings.TrimSpace(in.OldString) == "" {
				return "", fmt.Errorf("old_string must not be empty")
			}
			resolved, err := resolveToolPath(cwd, in.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", err
			}
			content := string(data)
			matches := strings.Count(content, in.OldString)
			if matches == 0 {
				return "", fmt.Errorf("old_string not found in %s", resolved)
			}
			if !in.ReplaceAll && matches != 1 {
				return "", fmt.Errorf("old_string matched %d times in %s; set replace_all to replace all matches", matches, resolved)
			}
			updated := strings.Replace(content, in.OldString, in.NewString, replacementCount(in.ReplaceAll))
			if err := os.WriteFile(resolved, []byte(updated), 0o644); err != nil {
				return "", err
			}
			if in.ReplaceAll {
				return toolResultJSON(map[string]any{"path": resolved, "replacements": matches})
			}
			return toolResultJSON(map[string]any{"path": resolved, "replacements": 1})
		},
	}
}

func newGlobTool(cwd string) Tool {
	type args struct {
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit,omitempty"`
	}
	return Tool{
		Name:        "glob",
		Description: "Find files by glob-like pattern relative to the sandbox working directory.",
		Parameters: jsonSchemaObject(map[string]any{
			"pattern": map[string]any{"type": "string"},
			"limit":   map[string]any{"type": "integer"},
		}, []string{"pattern"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode glob args: %w", err)
			}
			matcher, err := globPatternToRegexp(in.Pattern)
			if err != nil {
				return "", err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 200
			}
			matches := make([]string, 0, minInt(limit, 32))
			err = filepath.WalkDir(cwd, func(current string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if current == cwd {
					return nil
				}
				rel, err := filepath.Rel(cwd, current)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if d.IsDir() {
					if rel == ".git" || strings.HasPrefix(rel, ".git/") {
						return filepath.SkipDir
					}
					return nil
				}
				if matcher.MatchString(rel) {
					matches = append(matches, rel)
					if len(matches) >= limit {
						return errToolLimitReached
					}
				}
				return nil
			})
			if err != nil && err != errToolLimitReached {
				return "", err
			}
			sort.Strings(matches)
			return toolResultJSON(map[string]any{
				"pattern": in.Pattern,
				"matches": matches,
			})
		},
	}
}

func newGrepTool(cwd string) Tool {
	type args struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path,omitempty"`
		Limit           int    `json:"limit,omitempty"`
		CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	}
	return Tool{
		Name:        "grep",
		Description: "Search file contents for a regular expression.",
		Parameters: jsonSchemaObject(map[string]any{
			"pattern":          map[string]any{"type": "string"},
			"path":             map[string]any{"type": "string"},
			"limit":            map[string]any{"type": "integer"},
			"case_insensitive": map[string]any{"type": "boolean"},
		}, []string{"pattern"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode grep args: %w", err)
			}
			pattern := in.Pattern
			if in.CaseInsensitive {
				pattern = "(?i)" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "", err
			}
			searchRoot := cwd
			if strings.TrimSpace(in.Path) != "" {
				searchRoot, err = resolveToolPath(cwd, in.Path)
				if err != nil {
					return "", err
				}
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 100
			}
			var matches []map[string]any
			err = filepath.Walk(searchRoot, func(path string, info os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if info.IsDir() {
					if info.Name() == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				lines := strings.Split(string(data), "\n")
				for idx, line := range lines {
					if re.MatchString(line) {
						rel, _ := filepath.Rel(cwd, path)
						matches = append(matches, map[string]any{
							"path": filepath.ToSlash(rel),
							"line": idx + 1,
							"text": line,
						})
						if len(matches) >= limit {
							return errToolLimitReached
						}
					}
				}
				return nil
			})
			if err != nil && err != errToolLimitReached {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"pattern": in.Pattern,
				"matches": matches,
			})
		},
	}
}

func newBashTool(cwd string) Tool {
	type args struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms,omitempty"`
	}
	return Tool{
		Name:        "bash",
		Description: "Execute a shell command in the sandbox working directory.",
		Parameters: jsonSchemaObject(map[string]any{
			"command":    map[string]any{"type": "string"},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
		}, []string{"command"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode bash args: %w", err)
			}
			command := strings.TrimSpace(in.Command)
			if command == "" {
				return "", fmt.Errorf("command must not be empty")
			}
			timeout := 30 * time.Second
			if in.TimeoutMS > 0 {
				timeout = time.Duration(in.TimeoutMS) * time.Millisecond
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(runCtx, "/bin/sh", "-lc", command)
			cmd.Dir = cwd
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			exitCode := 0
			if cmd.ProcessState != nil {
				exitCode = cmd.ProcessState.ExitCode()
			}
			if runCtx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("bash timed out after %s", timeout)
			}
			output := stdout.String()
			if stderr.Len() > 0 {
				if output != "" {
					output += "\n"
				}
				output += stderr.String()
			}
			if err != nil && output == "" {
				output = err.Error()
			}
			return toolResultJSON(map[string]any{
				"command":   command,
				"exit_code": exitCode,
				"output":    strings.TrimSpace(output),
			})
		},
	}
}

func newGitStatusTool(cwd string) Tool {
	type args struct{}
	return Tool{
		Name:        "git_status",
		Description: "Run git status --short in the working directory.",
		Parameters:  jsonSchemaObject(map[string]any{}, nil, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			cmd := exec.CommandContext(ctx, "git", "status", "--short")
			cmd.Dir = cwd
			data, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(string(data)))
			}
			return toolResultJSON(map[string]any{
				"output": strings.TrimSpace(string(data)),
			})
		},
	}
}

func newGitDiffTool(cwd string) Tool {
	type args struct {
		Path string `json:"path,omitempty"`
	}
	return Tool{
		Name:        "git_diff",
		Description: "Run git diff, optionally scoped to a path.",
		Parameters: jsonSchemaObject(map[string]any{
			"path": map[string]any{"type": "string"},
		}, nil, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode git_diff args: %w", err)
			}
			argv := []string{"diff"}
			if strings.TrimSpace(in.Path) != "" {
				argv = append(argv, "--", in.Path)
			}
			cmd := exec.CommandContext(ctx, "git", argv...)
			cmd.Dir = cwd
			data, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(string(data)))
			}
			diff := string(data)
			if len(diff) > 100*1024 {
				diff = diff[:100*1024] + "\n\n[diff truncated — showing first 100KB]"
			}
			return toolResultJSON(map[string]any{
				"diff": diff,
			})
		},
	}
}

func resolveToolPath(cwd, userPath string) (string, error) {
	if strings.TrimSpace(userPath) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	base := filepath.Clean(cwd)
	if !filepath.IsAbs(userPath) {
		userPath = filepath.Join(base, userPath)
	}
	resolved := filepath.Clean(userPath)
	rel, err := filepath.Rel(base, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes working directory", userPath)
	}
	return resolved, nil
}

func globPatternToRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("pattern must not be empty")
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(".")
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b.WriteByte('\\')
			b.WriteByte(pattern[i])
		default:
			b.WriteByte(pattern[i])
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func replacementCount(replaceAll bool) int {
	if replaceAll {
		return -1
	}
	return 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var errToolLimitReached = fmt.Errorf("tool limit reached")
