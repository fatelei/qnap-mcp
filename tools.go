package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"qnap-mcp/qnap"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerTools registers all QNAP File Station tools on the MCP server.
func registerTools(s *server.MCPServer, c *qnap.Client) {
	// ---- connection / discovery ------------------------------------------
	s.AddTool(
		mcp.NewTool("qnap_test_connection",
			mcp.WithDescription("测试与 QNAP NAS 的连接和认证是否正常，返回共享文件夹数量。"),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			shares, err := c.ListShares(ctx)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(map[string]any{
				"connected":   true,
				"share_count": len(shares),
			}), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_list_shares",
			mcp.WithDescription("列出 QNAP NAS 上所有共享文件夹（share_root）。返回 text(名称)、id(路径，如 /Public)、cls(r=只读/w=读写)。"),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			shares, err := c.ListShares(ctx)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(map[string]any{"total": len(shares), "shares": shares}), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_list_folders",
			mcp.WithDescription("列出指定路径下的子文件夹树（get_tree）。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("文件夹路径，例如 /Public；根共享列表请用 qnap_list_shares。")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			nodes, err := c.ListFolders(ctx, p)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(map[string]any{"total": len(nodes), "folders": nodes}), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_list_files",
			mcp.WithDescription("列出指定文件夹中的文件和子文件夹（get_list），支持分页与排序。返回 total 和 datas，其中 isfolder=1 表示文件夹，filesize 单位为字节。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("文件夹路径，例如 /Public 或 /Public/Photos。")),
			mcp.WithInteger("limit", mcp.Description("每页返回数量，默认 1000。")),
			mcp.WithInteger("start", mcp.Description("起始索引，默认 0。")),
			mcp.WithString("sort", mcp.Description("排序字段：filename/filesize/filetype/mt/privilege/owner/group，默认 filename。")),
			mcp.WithString("dir", mcp.Description("排序方向：ASC 或 DESC，默认 ASC。")),
			mcp.WithBoolean("hidden_file", mcp.Description("是否包含隐藏文件，默认 false。")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.ListFiles(ctx, qnap.ListFilesInput{
				Path:       p,
				Limit:      req.GetInt("limit", 1000),
				Start:      req.GetInt("start", 0),
				Sort:       req.GetString("sort", "filename"),
				Dir:        req.GetString("dir", "ASC"),
				HiddenFile: req.GetBool("hidden_file", false),
			})
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_stat",
			mcp.WithDescription("获取一个或多个文件/文件夹的详细信息（大小、权限、所有者、修改时间等）。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("文件所在的文件夹路径，例如 /Public。")),
			mcp.WithArray("file_names", mcp.Required(), mcp.WithStringItems(), mcp.Description("要查询的文件/文件夹名称数组。")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			dir, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			names, err := stringSliceArg(req, "file_names")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.Stat(ctx, dir, names)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_search",
			mcp.WithDescription("在指定路径下按关键字搜索文件或文件夹（search）。返回的结果 filename 可能包含 /share/HDA_DATA/ 前缀，操作时需去掉。"),
			mcp.WithString("keyword", mcp.Required(), mcp.Description("搜索关键字。")),
			mcp.WithString("source_path", mcp.Required(), mcp.Description("搜索起始路径，例如 /Public。")),
			mcp.WithInteger("limit", mcp.Description("返回数量上限，默认 1000。")),
			mcp.WithInteger("start", mcp.Description("起始索引，默认 0。")),
			mcp.WithString("sort", mcp.Description("排序字段，默认 filename。")),
			mcp.WithString("dir", mcp.Description("排序方向 ASC/DESC，默认 ASC。")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			keyword, err := req.RequireString("keyword")
			if err != nil {
				return toolError(err), nil
			}
			source, err := req.RequireString("source_path")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.Search(ctx, keyword, source,
				req.GetInt("limit", 1000), req.GetInt("start", 0),
				req.GetString("sort", "filename"), req.GetString("dir", "ASC"))
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_get_file_size",
			mcp.WithDescription("统计指定路径中一个或多个文件/文件夹的总大小（字节）。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("文件/文件夹所在路径。")),
			mcp.WithArray("file_names", mcp.Required(), mcp.WithStringItems(), mcp.Description("要统计大小的名称数组。")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			dir, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			names, err := stringSliceArg(req, "file_names")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.GetFileSize(ctx, dir, names)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	// ---- mutations --------------------------------------------------------
	s.AddTool(
		mcp.NewTool("qnap_create_folder",
			mcp.WithDescription("在 QNAP 上创建新文件夹（createdir）。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("父文件夹路径，例如 /Public。")),
			mcp.WithString("name", mcp.Required(), mcp.Description("要创建的新文件夹名称。")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			name, err := req.RequireString("name")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.CreateFolder(ctx, p, name)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_rename",
			mcp.WithDescription("重命名 QNAP 上的文件或文件夹（rename）。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("文件/文件夹所在路径。")),
			mcp.WithString("source_name", mcp.Required(), mcp.Description("当前名称。")),
			mcp.WithString("dest_name", mcp.Required(), mcp.Description("新名称。")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			src, err := req.RequireString("source_name")
			if err != nil {
				return toolError(err), nil
			}
			dst, err := req.RequireString("dest_name")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.Rename(ctx, p, src, dst)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_copy",
			mcp.WithDescription("复制一个或多个文件/文件夹到目标路径（copy）。"),
			mcp.WithString("source_path", mcp.Required(), mcp.Description("源路径。")),
			mcp.WithString("dest_path", mcp.Required(), mcp.Description("目标路径。")),
			mcp.WithArray("file_names", mcp.Required(), mcp.WithStringItems(), mcp.Description("要复制的名称数组。")),
			mcp.WithInteger("mode", mcp.Description("0=覆盖同名文件，1=跳过同名文件（默认 1）。")),
			mcp.WithString("dup", mcp.Description("复制到同目录时的重名后缀，例如 copy。")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			src, err := req.RequireString("source_path")
			if err != nil {
				return toolError(err), nil
			}
			dst, err := req.RequireString("dest_path")
			if err != nil {
				return toolError(err), nil
			}
			names, err := stringSliceArg(req, "file_names")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.Copy(ctx, src, dst, names, req.GetInt("mode", 1), req.GetString("dup", ""))
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_move",
			mcp.WithDescription("移动一个或多个文件/文件夹到目标路径（move）。"),
			mcp.WithString("source_path", mcp.Required(), mcp.Description("源路径。")),
			mcp.WithString("dest_path", mcp.Required(), mcp.Description("目标路径。")),
			mcp.WithArray("file_names", mcp.Required(), mcp.WithStringItems(), mcp.Description("要移动的名称数组。")),
			mcp.WithInteger("mode", mcp.Description("0=覆盖同名文件，1=跳过同名文件（默认 1）。")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			src, err := req.RequireString("source_path")
			if err != nil {
				return toolError(err), nil
			}
			dst, err := req.RequireString("dest_path")
			if err != nil {
				return toolError(err), nil
			}
			names, err := stringSliceArg(req, "file_names")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.Move(ctx, src, dst, names, req.GetInt("mode", 1))
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_delete",
			mcp.WithDescription("⚠️ 删除 QNAP 上的文件或文件夹（delete）。此操作不可恢复（除非启用回收站）。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("文件/文件夹所在路径。")),
			mcp.WithArray("file_names", mcp.Required(), mcp.WithStringItems(), mcp.Description("要删除的名称数组。")),
			mcp.WithDestructiveHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			dir, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			names, err := stringSliceArg(req, "file_names")
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.Delete(ctx, dir, names)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(out), nil
		},
	)

	// ---- transfer ---------------------------------------------------------
	s.AddTool(
		mcp.NewTool("qnap_download",
			mcp.WithDescription("从 QNAP 下载文件到本地路径。单个文件默认传输原文件；多文件/文件夹返回 zip。"),
			mcp.WithString("path", mcp.Required(), mcp.Description("源文件所在路径，例如 /Public。")),
			mcp.WithString("file_name", mcp.Description("单个文件名（与 file_names 二选一）。")),
			mcp.WithArray("file_names", mcp.WithStringItems(), mcp.Description("多个文件名数组（与 file_name 二选一）。")),
			mcp.WithString("local_path", mcp.Description("本地保存路径；不填则保存到系统临时目录。")),
			mcp.WithBoolean("is_folder", mcp.Description("源是否为文件夹，默认 false。")),
			mcp.WithInteger("compress", mcp.Description("压缩方式：0=原文件/zip，2=压缩文件，默认 0。")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			dir, err := req.RequireString("path")
			if err != nil {
				return toolError(err), nil
			}
			names, err := downloadNames(req)
			if err != nil {
				return toolError(err), nil
			}
			local := strings.TrimSpace(req.GetString("local_path", ""))
			if local == "" {
				base := filepath.Base(names[0])
				local = filepath.Join(os.TempDir(), base)
			}
			local, err = filepath.Abs(local)
			if err != nil {
				return toolError(err), nil
			}
			if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
				return toolError(fmt.Errorf("创建本地目录失败: %w", err)), nil
			}
			local = uniquePath(local)
			f, err := os.Create(local)
			if err != nil {
				return toolError(fmt.Errorf("创建本地文件失败: %w", err)), nil
			}
			suggested, dlErr := c.DownloadTo(ctx, f, dir, names, req.GetBool("is_folder", false), req.GetInt("compress", 0))
			closeErr := f.Close()
			if dlErr != nil {
				_ = os.Remove(local)
				return toolError(dlErr), nil
			}
			if closeErr != nil {
				return toolError(fmt.Errorf("关闭本地文件失败: %w", closeErr)), nil
			}
			info, err := os.Stat(local)
			if err != nil {
				return toolError(err), nil
			}
			result := map[string]any{
				"local_path": local,
				"bytes":      info.Size(),
				"files":      names,
			}
			if suggested != "" {
				result["suggested_filename"] = suggested
			}
			return jsonResult(result), nil
		},
	)

	s.AddTool(
		mcp.NewTool("qnap_upload",
			mcp.WithDescription("上传本地单个文件到 QNAP 指定文件夹（upload）。"),
			mcp.WithString("local_path", mcp.Required(), mcp.Description("本地文件路径（必须是单个文件，不能是文件夹）。")),
			mcp.WithString("dest_path", mcp.Required(), mcp.Description("QNAP 上的目标文件夹路径，例如 /Public。")),
			mcp.WithBoolean("overwrite", mcp.Description("目标已存在时是否覆盖，默认 false（跳过）。")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			local, err := req.RequireString("local_path")
			if err != nil {
				return toolError(err), nil
			}
			dest, err := req.RequireString("dest_path")
			if err != nil {
				return toolError(err), nil
			}
			abs, err := filepath.Abs(local)
			if err != nil {
				return toolError(err), nil
			}
			out, err := c.Upload(ctx, abs, dest, req.GetBool("overwrite", false))
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(map[string]any{
				"status":     out.Status,
				"success":    out.Success,
				"local_path": abs,
				"dest_path":  dest,
				"filename":   filepath.Base(abs),
			}), nil
		},
	)
}

// --- helpers ----------------------------------------------------------------

func downloadNames(req mcp.CallToolRequest) ([]string, error) {
	var names []string
	if raw := strings.TrimSpace(req.GetString("file_name", "")); raw != "" {
		names = append(names, raw)
	}
	if _, ok := req.GetArguments()["file_names"]; ok {
		arr, err := stringSliceArg(req, "file_names")
		if err != nil {
			return nil, err
		}
		names = append(names, arr...)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("请提供 file_name 或 file_names 参数")
	}
	return names, nil
}

// stringSliceArg accepts either a JSON array of strings or a single string.
func stringSliceArg(req mcp.CallToolRequest, key string) ([]string, error) {
	raw, ok := req.GetArguments()[key]
	if !ok {
		return nil, fmt.Errorf("缺少参数 %s", key)
	}
	switch v := raw.(type) {
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("参数 %s 不能为空数组", key)
		}
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("参数 %s 必须是字符串数组", key)
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("参数 %s 不能为空数组", key)
		}
		return out, nil
	case string:
		if v == "" {
			return nil, fmt.Errorf("参数 %s 不能为空", key)
		}
		return []string{v}, nil
	default:
		return nil, fmt.Errorf("参数 %s 必须是字符串或字符串数组", key)
	}
}

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("序列化结果失败: " + err.Error())
	}
	return mcp.NewToolResultText(string(b))
}

func toolError(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(err.Error())
}

// uniquePath appends .1, .2, ... when local already exists.
func uniquePath(local string) string {
	if _, err := os.Stat(local); os.IsNotExist(err) {
		return local
	}
	ext := filepath.Ext(local)
	base := strings.TrimSuffix(local, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s.%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return local
}
