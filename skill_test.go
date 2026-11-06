package aicli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	aicli "github.com/redSun64/ai-cli"
)

func testSkill() aicli.SkillBundle {
	return aicli.SkillBundle{ID: "test", Files: fstest.MapFS{
		"SKILL.md":               {Data: []byte("---\nname: test\ndescription: Test business workflow.\n---\n\nRun {{ACLI_COMMAND}} schema.\n")},
		"references/recovery.md": {Data: []byte("Query status before retrying writes.")},
	}}
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSkillPreviewInstallAndForce(t *testing.T) {
	parent := realTempDir(t)
	bundle := testSkill()
	result, err := bundle.Install(aicli.SkillInstallOptions{Directory: parent, Launcher: "test-cli", DryRun: true})
	if err != nil || !result.DryRun || len(result.Files) != 2 {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(result.Directory); !os.IsNotExist(err) {
		t.Fatal("preview created directory")
	}
	result, err = bundle.Install(aicli.SkillInstallOptions{Directory: parent, Launcher: "test-cli"})
	if err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(result.Directory, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(main), "{{ACLI_COMMAND}}") || strings.Count(string(main), "## SDK 执行协议") != 1 || !strings.HasPrefix(string(main), "---\nname: test") {
		t.Fatal(string(main))
	}
	_, err = bundle.Install(aicli.SkillInstallOptions{Directory: parent, Launcher: "test-cli"})
	requireCode(t, err, "SKILL_ALREADY_EXISTS")
	if err := os.WriteFile(filepath.Join(result.Directory, "user-note.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = bundle.Install(aicli.SkillInstallOptions{Directory: parent, Launcher: "other-cli", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Directory, "user-note.txt")); err != nil {
		t.Fatal("force removed unrelated file")
	}
	if err := os.Remove(filepath.Join(result.Directory, "references", "recovery.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(result.Directory, "references", "recovery.md"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = bundle.Install(aicli.SkillInstallOptions{Directory: parent, Launcher: "test-cli", Force: true})
	requireCode(t, err, "SKILL_UNSAFE_PATH")
}

func TestSkillValidationAndCLI(t *testing.T) {
	for _, bundle := range []aicli.SkillBundle{
		{ID: "../escape", Files: testSkill().Files},
		{ID: "test", Files: fstest.MapFS{"SKILL.md": {Data: []byte("invalid")}}},
		{ID: "test", Files: fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: other\ndescription: Test\n---\n")}}},
		{ID: "test", Files: fstest.MapFS{"link": {Mode: os.ModeSymlink, Data: []byte("target")}}},
	} {
		_, err := aicli.New(aicli.Config{Name: "test", Skill: &bundle})
		requireCode(t, err, "SKILL_INVALID")
	}
	bundle := testSkill()
	r := runtimeFor(t, aicli.Config{Skill: &bundle})
	schema, err := r.Schema("skills", "install")
	if err != nil || len(schema.Options) != 3 {
		t.Fatal(schema, err)
	}
	parent := realTempDir(t)
	var out, errOut strings.Builder
	if code := r.RunCLI(context.Background(), []string{"skills", "install", "--dir", parent, "--dry-run"}, &out, &errOut, aicli.CLIOptions{}); code != 0 {
		t.Fatal(code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(parent, "test")); !os.IsNotExist(err) {
		t.Fatal("CLI preview wrote files")
	}
}

func TestSkillRejectsSymlinkAncestor(t *testing.T) {
	root := realTempDir(t)
	real := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	_, err := testSkill().Install(aicli.SkillInstallOptions{Directory: link, Launcher: "test-cli"})
	requireCode(t, err, "SKILL_UNSAFE_PATH")
}
