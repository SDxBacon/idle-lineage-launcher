//go:build ignore

// 此腳本供 Taskfile 讀取 build/config.yml 中的版本號，請勿手動執行。
// 跨平台替代 grep + sed 指令。
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
)

var configVersionRe = regexp.MustCompile(`^  version:[\t ]*(["']?)(\d+\.\d+\.\d+)(["']?)(?:[\t ]*#.*)?$`)

func main() {
	f, err := os.Open("build/config.yml")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		match := configVersionRe.FindStringSubmatch(scanner.Text())
		if match != nil {
			if match[1] != match[3] {
				fmt.Fprintln(os.Stderr, "build/config.yml 的 version 引號不成對")
				os.Exit(1)
			}
			fmt.Print(match[2])
			return
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		fmt.Fprintln(os.Stderr, "build/config.yml 中找不到 version 欄位")
	}
	os.Exit(1)
}
