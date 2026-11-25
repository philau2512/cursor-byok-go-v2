package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// stringList cho phép lặp -input nhiều lần, mỗi lần thêm một bundle. Không dùng
// dấu phân cách bên trong một giá trị vì đường dẫn Windows lẫn Unix đều có thể
// chứa dấu chấm phẩy hoặc hai chấm.
type stringList []string

func (list *stringList) String() string { return strings.Join(*list, " | ") }

func (list *stringList) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	*list = append(*list, trimmed)
	return nil
}

func bailIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func findPrettier() (string, error) {
	// 尝试常见的 prettier 命令名
	names := []string{"prettier", "prettier.cmd", "npx"}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("prettier not found in PATH, please install: npm install -g prettier")
}

// copyToTemp sao bundle sang temp để bước format không bao giờ đụng file gốc.
func copyToTemp(inputPath string) string {
	originalFile, err := os.Open(inputPath)
	bailIf(err)

	tempFile, err := os.CreateTemp(os.TempDir(), "cursor-source-*.js")
	bailIf(err)
	tempFileName := tempFile.Name()

	_, err = io.Copy(tempFile, originalFile)
	bailIf(err)

	bailIf(originalFile.Close())
	bailIf(tempFile.Close())
	return tempFileName
}

func formatWithPrettier(tempFileName string) {
	prettierBin, err := findPrettier()
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
		fmt.Println("Skipping formatting, extraction may be less accurate...")
		return
	}
	fmt.Println("Formatting file (this may take a while)...")
	var prettierCmd *exec.Cmd
	if filepath.Base(prettierBin) == "npx" {
		prettierCmd = exec.Command(prettierBin, "prettier", "--write", tempFileName)
	} else {
		prettierCmd = exec.Command(prettierBin, "--write", tempFileName)
	}
	out, err := prettierCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Prettier output: %s\n", string(out))
		fmt.Println("Warning: formatting failed, continuing anyway...")
		return
	}
	fmt.Println("Formatting complete")
}

func main() {
	inputs := &stringList{}
	flag.Var(inputs, "input", "Path to a Cursor bundle JS; lặp lại flag này để merge nhiều bundle")
	outputDir := flag.String("output", "", "Output directory for proto files (default: ./cursor_proto)")
	skipFormat := flag.Bool("skip-format", false, "Skip prettier formatting")
	strict := flag.Bool("strict", true, "Fail when extraction validation detects unresolved/placeholder output")
	flag.Parse()

	// Nhận thêm vị trí tham số để giữ cách gọi cũ: ext <file.js>
	for _, argument := range flag.Args() {
		bailIf(inputs.Set(argument))
	}

	if len(*inputs) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ext -input <path-to-js-file> [-input <another-bundle>] [-output <dir>] [-skip-format]")
		fmt.Fprintln(os.Stderr, "       ext <path-to-js-file> <path-to-another-bundle>")
		fmt.Fprintln(os.Stderr, "\nExample:")
		fmt.Fprintln(os.Stderr, "  ext -input /path/to/cursor-always-local/dist/main.js")
		fmt.Fprintln(os.Stderr, "  ext -input A/dist/main.js -input B/dist/main.js -output ./from_extensions")
		os.Exit(1)
	}

	for _, inputPath := range *inputs {
		info, err := os.Stat(inputPath)
		bailIf(err)
		if info.IsDir() {
			bailIf(fmt.Errorf("expected %s to be file, is dir", inputPath))
		}
	}

	if *outputDir == "" {
		wd, err := os.Getwd()
		bailIf(err)
		*outputDir = filepath.Join(wd, "cursor_proto")
	}

	fmt.Println("Copying source file to temp directory...")
	tempFiles := make([]string, 0, len(*inputs))
	for _, inputPath := range *inputs {
		tempFiles = append(tempFiles, copyToTemp(inputPath))
	}
	defer func() {
		for _, tempFileName := range tempFiles {
			os.Remove(tempFileName)
		}
	}()
	fmt.Printf("Temp files: %d\n", len(tempFiles))

	if !*skipFormat {
		for _, tempFileName := range tempFiles {
			formatWithPrettier(tempFileName)
		}
	} else {
		fmt.Println("Skipping formatting (--skip-format)")
	}

	fmt.Println("Extracting Proto definitions...")
	SetStrictMode(*strict)
	ExtractProtosFromFiles(tempFiles, *outputDir)

	fmt.Printf("\nOutput directory: %s\n", *outputDir)
}
