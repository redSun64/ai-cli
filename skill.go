package aicli

import (
	"bytes"
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

//go:embed skill-protocol.md
var skillProtocol string

// SkillBundle embeds one business Skill directory (SKILL.md and optional
// references/, scripts/, assets/). Use fs.Sub with go:embed for native delivery.
type SkillBundle struct {
	ID    string
	Files fs.FS
	files map[string][]byte
}

func (b SkillBundle) snapshot() (*SkillBundle, error) {
	if !domainID.MatchString(b.ID) || len(b.ID) > 64 || b.Files == nil {
		return nil, failure("SKILL_INVALID", "A valid Skill ID and filesystem are required.")
	}
	copy := &SkillBundle{ID: b.ID, files: map[string][]byte{}}
	total := 0
	err := fs.WalkDir(b.Files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") || entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("invalid entry")
		}
		parts := strings.Split(name, "/")
		if (parts[0] != "SKILL.md" && parts[0] != "references" && parts[0] != "scripts" && parts[0] != "assets") || (parts[0] == "SKILL.md" && (entry.IsDir() || len(parts) != 1)) {
			return fmt.Errorf("invalid Skill entry")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 || len(copy.files) >= 256 {
			return fmt.Errorf("invalid file or bundle too large")
		}
		content, err := fs.ReadFile(b.Files, name)
		if err != nil {
			return err
		}
		total += len(content)
		if total > 16<<20 {
			return fmt.Errorf("bundle too large")
		}
		copy.files[name] = content
		return nil
	})
	if err != nil {
		return nil, failure("SKILL_INVALID", "Could not read a valid bounded Skill bundle.")
	}
	main := strings.ReplaceAll(string(copy.files["SKILL.md"]), "\r\n", "\n")
	if !strings.HasPrefix(main, "---\n") {
		return nil, failure("SKILL_INVALID", "SKILL.md requires frontmatter.")
	}
	end := strings.Index(main[4:], "\n---\n")
	if end < 0 {
		return nil, failure("SKILL_INVALID", "SKILL.md requires closing frontmatter.")
	}
	front := main[4 : 4+end]
	nameOK, descriptionOK := false, false
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if key == "name" {
			nameOK = value == b.ID
		}
		if key == "description" {
			descriptionOK = value != "" && len([]rune(value)) <= 1024 && value != "|" && value != ">"
		}
	}
	if !nameOK || !descriptionOK {
		return nil, failure("SKILL_INVALID", "Frontmatter needs a plain name matching ID and a single-line description.")
	}
	cut := 4 + end + 5
	copy.files["SKILL.md"] = []byte(main[:cut] + "\n" + skillProtocol + "\n" + main[cut:])
	return copy, nil
}

// SkillInstallOptions chooses the parent directory and overwrite/preview policy.
// Launcher is a shell-quoted CLI invocation inserted in the SDK protocol.
// The CLI supplies the absolute executable path automatically.
type SkillInstallOptions struct {
	Directory string
	Launcher  string
	DryRun    bool
	Force     bool
}

// SkillInstallResult describes files managed by this bundle.
type SkillInstallResult struct {
	Directory string   `json:"directory"`
	Files     []string `json:"files"`
	DryRun    bool     `json:"dryRun"`
}

// Install renders and installs the Skill. Symlink/reparse-like non-directory
// ancestors and symlink destinations are rejected. Force replaces bundle files
// only; unrelated files remain. Replacement is atomic per file, not per bundle.
// The destination must be owned by the current user and not concurrently modified
// by other processes during installation.
func (b SkillBundle) Install(options SkillInstallOptions) (SkillInstallResult, error) {
	if b.files == nil {
		snapshot, err := b.snapshot()
		if err != nil {
			return SkillInstallResult{}, err
		}
		return snapshot.Install(options)
	}
	if options.Launcher == "" || strings.ContainsAny(options.Launcher, "\r\n") {
		return SkillInstallResult{}, failure("SKILL_INVALID", "A single-line launcher is required.")
	}
	parent := options.Directory
	if parent == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return SkillInstallResult{}, failure("SKILL_INSTALL_FAILED", "Could not locate the user home directory.")
		}
		parent = filepath.Join(home, ".agents", "skills")
	}
	target, err := filepath.Abs(filepath.Join(parent, b.ID))
	if err != nil {
		return SkillInstallResult{}, failure("SKILL_INSTALL_FAILED", "Invalid installation directory.")
	}
	if err := checkAncestors(target); err != nil {
		return SkillInstallResult{}, err
	}
	if _, err := os.Lstat(target); err == nil && !options.Force {
		return SkillInstallResult{}, failure("SKILL_ALREADY_EXISTS", "Skill already exists; inspect it before using --force.")
	} else if err != nil && !os.IsNotExist(err) {
		return SkillInstallResult{}, failure("SKILL_INSTALL_FAILED", "Could not inspect installation directory.")
	}
	result := SkillInstallResult{Directory: target, DryRun: options.DryRun}
	for name := range b.files {
		result.Files = append(result.Files, name)
	}
	sort.Strings(result.Files)
	// Preflight every destination before writing the first file.
	for _, name := range result.Files {
		file := filepath.Join(target, filepath.FromSlash(name))
		if err := checkAncestors(filepath.Dir(file)); err != nil {
			return SkillInstallResult{}, err
		}
		if info, err := os.Lstat(file); err == nil && !info.Mode().IsRegular() {
			return SkillInstallResult{}, failure("SKILL_UNSAFE_PATH", "Skill destination must be a regular file.")
		} else if err != nil && !os.IsNotExist(err) {
			return SkillInstallResult{}, failure("SKILL_INSTALL_FAILED", "Could not inspect Skill destination.")
		}
	}
	if options.DryRun {
		return result, nil
	}
	for _, name := range result.Files {
		file := filepath.Join(target, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return SkillInstallResult{}, failure("SKILL_INSTALL_FAILED", "Could not create Skill directory.")
		}
		content := b.files[name]
		if strings.HasSuffix(name, ".md") {
			content = bytes.ReplaceAll(content, []byte("{{ACLI_COMMAND}}"), []byte(options.Launcher))
		}
		if err := writeSkillFile(file, content, strings.HasPrefix(name, "scripts/")); err != nil {
			return SkillInstallResult{}, failure("SKILL_INSTALL_FAILED", "Could not write Skill file; installation may be partial.")
		}
	}
	return result, nil
}

func checkAncestors(dir string) error {
	for {
		info, err := os.Lstat(dir)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return failure("SKILL_UNSAFE_PATH", "Installation ancestors must be real directories.")
			}
		} else if !os.IsNotExist(err) {
			return failure("SKILL_INSTALL_FAILED", "Could not inspect installation path.")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func writeSkillFile(dest string, content []byte, executable bool) error {
	file, err := os.CreateTemp(filepath.Dir(dest), ".aicli-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	mode := fs.FileMode(0600)
	if executable {
		mode = 0700
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), dest)
}

func executableCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", failure("SKILL_INSTALL_FAILED", "Could not locate CLI executable.")
	}
	if runtime.GOOS == "windows" {
		return "& '" + strings.ReplaceAll(exe, "'", "''") + "'", nil
	}
	return "'" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "'", nil
}
