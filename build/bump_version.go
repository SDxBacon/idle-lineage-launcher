//go:build ignore

// 此腳本供 Taskfile 的 bump 相關任務使用，請勿手動執行。
// 跨平台替代 sed 指令，支援語意化版本遞增。
//
// 用法：
//
//	go run ./build/bump_version.go patch       # 0.1.0 → 0.1.1
//	go run ./build/bump_version.go minor       # 0.1.0 → 0.2.0
//	go run ./build/bump_version.go major       # 0.1.0 → 1.0.0
//	go run ./build/bump_version.go 1.2.3       # 直接設定為 1.2.3
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
)

var (
	versionRe       = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	configVersionRe = regexp.MustCompile(`(?m)^(  version:[\t ]*)(["']?)(\d+\.\d+\.\d+)(["']?)([\t ]*(?:#.*)?)$`)
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./build/bump_version.go <patch|minor|major|x.y.z>")
		os.Exit(1)
	}

	const configPath = "build/config.yml"
	current, err := readVersion(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "讀取版本失敗:", err)
		os.Exit(1)
	}

	next, err := resolveNextVersion(current, os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := writeVersion(configPath, current, next); err != nil {
		fmt.Fprintln(os.Stderr, "寫入版本失敗:", err)
		os.Exit(1)
	}

	fmt.Printf("Bumped: %s → %s\n", current, next)
}

func readVersion(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		match := configVersionRe.FindStringSubmatch(scanner.Text())
		if match != nil {
			if match[2] != match[4] {
				return "", fmt.Errorf("config.yml 的 version 引號不成對")
			}
			return match[3], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("config.yml 中找不到 version 欄位")
}

func resolveNextVersion(current, arg string) (string, error) {
	match := versionRe.FindStringSubmatch(current)
	if match == nil {
		return "", fmt.Errorf("目前版本格式不合法: %s", current)
	}

	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])

	switch arg {
	case "patch":
		patch++
	case "minor":
		minor++
		patch = 0
	case "major":
		major++
		minor = 0
		patch = 0
	default:
		if !versionRe.MatchString(arg) {
			return "", fmt.Errorf("版本格式不合法（需為 x.y.z 或 patch/minor/major）: %s", arg)
		}
		return arg, nil
	}

	return fmt.Sprintf("%d.%d.%d", major, minor, patch), nil
}

func writeVersion(path, current, next string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	replaced := 0
	updated := configVersionRe.ReplaceAllStringFunc(string(data), func(line string) string {
		match := configVersionRe.FindStringSubmatch(line)
		if match == nil || match[3] != current || match[2] != match[4] {
			return line
		}
		replaced++
		return match[1] + match[2] + next + match[4] + match[5]
	})
	if replaced != 1 {
		return fmt.Errorf("預期更新 1 個 version 欄位，實際更新 %d 個", replaced)
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), info.Mode().Perm())
}
