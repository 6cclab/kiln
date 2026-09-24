package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
)

// Ported from test/mentions.test.ts.

var (
	small = budget.TierForWindow(32_768)
	large = budget.TierForWindow(200_000)
)

func TestParseMentionsFindsMentionAtStartAndMidSentence(t *testing.T) {
	assertStrings(t, ParseMentions("@src/a.ts what does this do"), []string{"src/a.ts"})
	assertStrings(t, ParseMentions("compare @a.ts and @b.ts"), []string{"a.ts", "b.ts"})
}

func TestParseMentionsStripsTrailingPunctuation(t *testing.T) {
	assertStrings(t, ParseMentions("look at @src/a.ts, then @b.ts."), []string{"src/a.ts", "b.ts"})
}

func TestParseMentionsDoesNotTreatEmailAsMention(t *testing.T) {
	assertStrings(t, ParseMentions("mail me at someone@example.com"), nil)
}

func TestParseMentionsDeduplicates(t *testing.T) {
	assertStrings(t, ParseMentions("@a.ts vs @a.ts"), []string{"a.ts"})
}

func TestParseMentionsFindsNothingWithoutMention(t *testing.T) {
	assertStrings(t, ParseMentions("what does the editor do?"), nil)
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func setupMentionDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.ts"), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("const line")
		b.WriteString(itoa(i))
		b.WriteString(" = ")
		b.WriteString(itoa(i))
		b.WriteByte(';')
	}
	if err := os.WriteFile(filepath.Join(dir, "big.ts"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestResolveMentionsInlinesFileContents(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("@small.ts explain", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Prompt, "export const x = 1;") {
		t.Fatalf("contents not inlined: %s", out.Prompt)
	}
	if !strings.Contains(out.Prompt, `<file path="small.ts">`) {
		t.Fatalf("missing file wrapper: %s", out.Prompt)
	}
	if !strings.HasSuffix(strings.TrimRight(out.Prompt, "\n"), "@small.ts explain") {
		t.Fatalf("question must survive and come last: %s", out.Prompt)
	}
}

func TestResolveMentionsLeavesLineWithNoMentionUntouched(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("just a question", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if out.Prompt != "just a question" {
		t.Fatalf("got %q", out.Prompt)
	}
	if len(out.Mentions) != 0 {
		t.Fatalf("Mentions = %+v", out.Mentions)
	}
}

func TestResolveMentionsCapsLargeFileToTierBudget(t *testing.T) {
	dir := setupMentionDir(t)
	big, err := os.ReadFile(filepath.Join(dir, "big.ts"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ResolveMentions("@big.ts explain", Options{Cwd: dir, Tier: &small})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mentions[0]
	if !m.Truncated {
		t.Fatal("was not truncated")
	}
	if m.Tokens > small.ToolOutputTokens {
		t.Fatalf("%d over %d", m.Tokens, small.ToolOutputTokens)
	}
	if len(out.Prompt) >= len(big) {
		t.Fatal("prompt carried the whole file")
	}
}

func TestResolveMentionsTellsModelFileWasCut(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("@big.ts explain", Options{Cwd: dir, Tier: &small})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Prompt, "truncated") {
		t.Fatal("no truncation notice")
	}
	if !strings.Contains(out.Prompt, "read tool") {
		t.Fatal("no pointer to the rest")
	}
}

func TestResolveMentionsSplitsBudgetAcrossMentions(t *testing.T) {
	dir := setupMentionDir(t)
	one, err := ResolveMentions("@big.ts", Options{Cwd: dir, Tier: &small})
	if err != nil {
		t.Fatal(err)
	}
	three, err := ResolveMentions("@big.ts @big.ts @big.ts", Options{Cwd: dir, Tier: &small})
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]Mention{}, one.Mentions...), three.Mentions...)
	for _, m := range all {
		if m.Tokens > small.ToolOutputTokens {
			t.Fatalf("mention over budget: %+v", m)
		}
	}
}

func TestResolveMentionsReportsMissingFile(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("@nope.ts explain", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mentions[0].Skipped != "not found" {
		t.Fatalf("Skipped = %q", out.Mentions[0].Skipped)
	}
	if out.Prompt != "@nope.ts explain" {
		t.Fatalf("prompt should be unchanged: %q", out.Prompt)
	}
}

func TestResolveMentionsDoesNotInlineDirectory(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("@sub explain", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mentions[0].Skipped != "is a directory" {
		t.Fatalf("Skipped = %q", out.Mentions[0].Skipped)
	}
}

func TestResolveMentionsRefusesPathOutsideWorkspace(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("@/etc/hosts explain", Options{Cwd: dir, Tier: &large, Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mentions[0].Skipped != "outside the workspace" {
		t.Fatalf("Skipped = %q", out.Mentions[0].Skipped)
	}
	if strings.Contains(out.Prompt, "<file") {
		t.Fatalf("prompt should not inline the file: %q", out.Prompt)
	}
}

func TestResolveMentionsDescribesWithoutEchoingContent(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("@small.ts explain", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	described := strings.Join(DescribeMentions(out.Mentions, dir), "\n")
	if !strings.Contains(described, "small.ts") {
		t.Fatalf("described = %q", described)
	}
	if !strings.Contains(described, "tokens") {
		t.Fatalf("described = %q", described)
	}
	if strings.Contains(described, "export const x") {
		t.Fatalf("echoed the file contents: %q", described)
	}
}

// A 1x1 red PNG, so this exercises real bytes rather than a stub.
const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func setupImageDir(t *testing.T) (string, []byte) {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString(pngB64)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shot.JPG"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, png
}

func TestImageAttachesInsteadOfInlining(t *testing.T) {
	dir, png := setupImageDir(t)
	out, err := ResolveMentions("@shot.png what is this?", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 1 {
		t.Fatalf("Images = %+v", out.Images)
	}
	if out.Images[0].MimeType != "image/png" {
		t.Fatalf("MimeType = %q", out.Images[0].MimeType)
	}
	if out.Images[0].Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatal("image data mismatch")
	}
	if out.Prompt != "@shot.png what is this?" {
		t.Fatalf("prompt must be untouched: %q", out.Prompt)
	}
}

func TestImageExtensionCaseInsensitive(t *testing.T) {
	dir, _ := setupImageDir(t)
	out, err := ResolveMentions("@shot.JPG", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Images) == 0 || out.Images[0].MimeType != "image/jpeg" {
		t.Fatalf("Images = %+v", out.Images)
	}
}

func TestImageStillInlinesTextAlongsideImage(t *testing.T) {
	dir, _ := setupImageDir(t)
	out, err := ResolveMentions("@shot.png @notes.txt compare", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 1 {
		t.Fatalf("Images = %+v", out.Images)
	}
	if !strings.Contains(out.Prompt, "plain text") {
		t.Fatalf("text file was not inlined: %q", out.Prompt)
	}
}

func TestImageDescribedBySizeNotTokens(t *testing.T) {
	dir, _ := setupImageDir(t)
	out, err := ResolveMentions("@shot.png", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	described := strings.Join(DescribeMentions(out.Mentions, dir), "")
	if !strings.Contains(described, "image") {
		t.Fatalf("described = %q", described)
	}
	if strings.Contains(described, "tokens") {
		t.Fatalf("image costed in tokens: %q", described)
	}
}

func TestImageReturnsNoImagesWhenNoneMentioned(t *testing.T) {
	dir, _ := setupImageDir(t)
	out, err := ResolveMentions("@notes.txt", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 0 {
		t.Fatalf("Images = %+v", out.Images)
	}
	out2, err := ResolveMentions("nothing here", Options{Cwd: dir, Tier: &large})
	if err != nil {
		t.Fatal(err)
	}
	if len(out2.Images) != 0 {
		t.Fatalf("Images = %+v", out2.Images)
	}
}

func TestImageOutsideWorkspaceNotAttached(t *testing.T) {
	dir, _ := setupImageDir(t)
	out, err := ResolveMentions("@/etc/passwd.png", Options{Cwd: dir, Tier: &large, Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 0 {
		t.Fatalf("Images = %+v", out.Images)
	}
	if out.Mentions[0].Skipped != "outside the workspace" {
		t.Fatalf("Skipped = %q", out.Mentions[0].Skipped)
	}
}
