package split

import (
	"fmt"
	"strings"
)

// Readme returns human-readable, bilingual instructions for joining the parts
// manually. It is written next to the parts for users who want to recover their
// data without Cloudnest.
func Readme(m Manifest) string {
	var b strings.Builder
	b.WriteString("Cloudnest split backup / 雲栖切片備份\n")
	b.WriteString("====================================\n\n")
	fmt.Fprintf(&b, "Original file / 原始檔案: %s\n", m.Name)
	fmt.Fprintf(&b, "Size / 大小: %d bytes\n", m.Size)
	fmt.Fprintf(&b, "Parts / 份數: %d\n", m.PartCount)
	fmt.Fprintf(&b, "Whole-file SHA-256 / 全檔雜湊: %s\n\n", m.SHA256)

	b.WriteString("IMPORTANT / 重要:\n")
	b.WriteString("- manifest.json is uploaded last and marks the set as complete.\n")
	b.WriteString("  manifest.json 最後才上傳，代表整組已上傳完成，請勿修改或刪除。\n")
	b.WriteString("- Put all part files in one folder before joining.\n")
	b.WriteString("  合併前，請把所有 part 檔放在同一個資料夾。\n")
	b.WriteString("- Join the parts in numeric order, then verify the SHA-256.\n")
	b.WriteString("  請依編號順序合併，完成後比對 SHA-256。\n\n")

	b.WriteString("Windows (Command Prompt / 命令提示字元):\n")
	if len(m.Parts) == 0 {
		b.WriteString("  (no parts listed / 沒有列出任何部分)\n")
	} else {
		names := make([]string, 0, len(m.Parts))
		for _, p := range m.Parts {
			names = append(names, p.Name)
		}
		fmt.Fprintf(&b, "  copy /b %s \"%s\"\n", strings.Join(names, " + "), m.Name)
	}
	b.WriteString("\n")

	b.WriteString("macOS / Linux:\n")
	if len(m.Parts) == 0 {
		b.WriteString("  (no parts listed / 沒有列出任何部分)\n")
	} else {
		names := make([]string, 0, len(m.Parts))
		for _, p := range m.Parts {
			names = append(names, p.Name)
		}
		fmt.Fprintf(&b, "  cat %s > \"%s\"\n", strings.Join(names, " "), m.Name)
	}
	b.WriteString("\n")

	b.WriteString("Verify / 驗證:\n")
	b.WriteString("  Windows: certutil -hashfile \"")
	b.WriteString(m.Name)
	b.WriteString("\" SHA256\n")
	b.WriteString("  macOS:   shasum -a 256 \"")
	b.WriteString(m.Name)
	b.WriteString("\"\n")
	b.WriteString("  Linux:   sha256sum \"")
	b.WriteString(m.Name)
	b.WriteString("\"\n")
	return b.String()
}
