package runtime

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed prompt_defaults/*.md
var promptDefaultsFS embed.FS

type PromptDescriptor struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Source  string `json:"source"`
	Path    string `json:"path"`
}

type PromptStore struct {
	root string
}

func NewPromptStore(root string) *PromptStore {
	return &PromptStore{root: root}
}

func promptRoles() []string {
	return []string{
		AgentProfileConductor,
		AgentProfileVText,
		AgentProfileResearcher,
		AgentProfileSuper,
		AgentProfileCoSuper,
	}
}

func (ps *PromptStore) List(ownerID string) ([]PromptDescriptor, error) {
	if err := ps.ensureDefaults(); err != nil {
		return nil, err
	}
	prompts := make([]PromptDescriptor, 0, len(promptRoles()))
	for _, role := range promptRoles() {
		prompt, err := ps.Load(ownerID, role)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, prompt)
	}
	sort.Slice(prompts, func(i, j int) bool {
		return prompts[i].Role < prompts[j].Role
	})
	return prompts, nil
}

func (ps *PromptStore) Load(ownerID, role string) (PromptDescriptor, error) {
	if err := ps.ensureDefaults(); err != nil {
		return PromptDescriptor{}, err
	}
	role, err := normalizePromptRole(role)
	if err != nil {
		return PromptDescriptor{}, err
	}
	userPath := ps.userPromptPath(ownerID, role)
	if ownerID != "" {
		if content, err := os.ReadFile(userPath); err == nil {
			return PromptDescriptor{
				Role:    role,
				Content: strings.TrimSpace(string(content)),
				Source:  "user",
				Path:    userPath,
			}, nil
		} else if !os.IsNotExist(err) {
			return PromptDescriptor{}, fmt.Errorf("read user prompt %s: %w", role, err)
		}
	}
	defaultPath := ps.defaultPromptPath(role)
	content, err := os.ReadFile(defaultPath)
	if err != nil {
		return PromptDescriptor{}, fmt.Errorf("read default prompt %s: %w", role, err)
	}
	return PromptDescriptor{
		Role:    role,
		Content: strings.TrimSpace(string(content)),
		Source:  "default",
		Path:    defaultPath,
	}, nil
}

func (ps *PromptStore) Save(ownerID, role, content string) (PromptDescriptor, error) {
	if strings.TrimSpace(ownerID) == "" {
		return PromptDescriptor{}, fmt.Errorf("owner is required")
	}
	if err := ps.ensureDefaults(); err != nil {
		return PromptDescriptor{}, err
	}
	role, err := normalizePromptRole(role)
	if err != nil {
		return PromptDescriptor{}, err
	}
	if strings.TrimSpace(content) == "" {
		return PromptDescriptor{}, fmt.Errorf("prompt content is required")
	}
	path := ps.userPromptPath(ownerID, role)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return PromptDescriptor{}, fmt.Errorf("create prompt directory: %w", err)
	}
	normalized := strings.TrimSpace(content) + "\n"
	if err := os.WriteFile(path, []byte(normalized), 0o644); err != nil {
		return PromptDescriptor{}, fmt.Errorf("write prompt override: %w", err)
	}
	return PromptDescriptor{
		Role:    role,
		Content: strings.TrimSpace(content),
		Source:  "user",
		Path:    path,
	}, nil
}

func (ps *PromptStore) Reset(ownerID, role string) (PromptDescriptor, error) {
	if strings.TrimSpace(ownerID) == "" {
		return PromptDescriptor{}, fmt.Errorf("owner is required")
	}
	role, err := normalizePromptRole(role)
	if err != nil {
		return PromptDescriptor{}, err
	}
	path := ps.userPromptPath(ownerID, role)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return PromptDescriptor{}, fmt.Errorf("remove prompt override: %w", err)
	}
	return ps.Load(ownerID, role)
}

func normalizePromptRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	for _, allowed := range promptRoles() {
		if role == allowed {
			return role, nil
		}
	}
	return "", fmt.Errorf("unsupported prompt role %q", role)
}

func (ps *PromptStore) ensureDefaults() error {
	if strings.TrimSpace(ps.root) == "" {
		return fmt.Errorf("prompt root is not configured")
	}
	if err := os.MkdirAll(filepath.Join(ps.root, "defaults"), 0o755); err != nil {
		return fmt.Errorf("create prompt defaults directory: %w", err)
	}
	for _, role := range promptRoles() {
		path := ps.defaultPromptPath(role)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat prompt default %s: %w", role, err)
		}
		content, err := fs.ReadFile(promptDefaultsFS, filepath.ToSlash(filepath.Join("prompt_defaults", role+".md")))
		if err != nil {
			return fmt.Errorf("load embedded prompt default %s: %w", role, err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return fmt.Errorf("seed prompt default %s: %w", role, err)
		}
	}
	return nil
}

func (ps *PromptStore) defaultPromptPath(role string) string {
	return filepath.Join(ps.root, "defaults", role+".md")
}

func (ps *PromptStore) userPromptPath(ownerID, role string) string {
	return filepath.Join(ps.root, "users", sanitizePromptPath(ownerID), role+".md")
}

func sanitizePromptPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "anonymous"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("-_.@", r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "anonymous"
	}
	return b.String()
}
