// Command qnap-mcp is an MCP server that exposes the QNAP QTS File Station
// API as MCP tools over stdio, so MCP clients such as WorkBuddy can operate
// files on a QNAP NAS.
package main

import (
	"context"
	"fmt"
	"os"

	"qnap-mcp/qnap"

	"github.com/mark3labs/mcp-go/server"
)

const version = "1.0.0"

func main() {
	// `qnap-mcp --check` performs a login + list-shares smoke test, useful
	// when configuring environment variables before registering the server.
	if len(os.Args) > 1 && (os.Args[1] == "--check" || os.Args[1] == "check") {
		checkConnection()
		return
	}

	cfg, err := qnap.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "qnap-mcp 配置错误: %v\n", err)
		os.Exit(1)
	}
	client, err := qnap.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qnap-mcp 初始化失败: %v\n", err)
		os.Exit(1)
	}

	s := server.NewMCPServer(
		"qnap-mcp",
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	registerTools(s, client)

	// 所有日志输出到 stderr；stdout 保留给 MCP 协议。
	fmt.Fprintf(os.Stderr, "qnap-mcp v%s 已启动 (host=%s, user=%s)\n", version, cfg.Host, cfg.User)
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "qnap-mcp 服务错误: %v\n", err)
		os.Exit(1)
	}
}

func checkConnection() {
	cfg, err := qnap.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(1)
	}
	client, err := qnap.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	shares, err := client.ListShares(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("连接成功，共 %d 个共享文件夹：\n", len(shares))
	for _, s := range shares {
		fmt.Printf("  %-24s %-24s %s\n", s.Text, s.ID, s.Cls)
	}
}
