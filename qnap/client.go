// Package qnap implements a minimal client for the QNAP QTS File Station
// Web API (cgi-bin/authLogin.cgi + cgi-bin/filemanager/utilRequest.cgi).
package qnap

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout  = 30 * time.Second
	utilRequestPath = "/cgi-bin/filemanager/utilRequest.cgi"
	authLoginPath   = "/cgi-bin/authLogin.cgi"
	maxResponseSize = 8 << 20 // 8 MiB for JSON control responses
)

// Config holds the connection settings for a QNAP NAS.
type Config struct {
	Host        string // e.g. "192.168.1.10:8080", "https://nas.example.com"
	Scheme      string // http or https; used when Host has no scheme
	User        string
	Password    string
	InsecureTLS bool // skip TLS certificate verification (self-signed NAS certs)
	Timeout     time.Duration
	SID         string // optional pre-obtained session id; skips login
}

// ConfigFromEnv reads configuration from QNAP_* environment variables.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Host:        strings.TrimSpace(os.Getenv("QNAP_HOST")),
		Scheme:      strings.ToLower(strings.TrimSpace(os.Getenv("QNAP_SCHEME"))),
		User:        strings.TrimSpace(os.Getenv("QNAP_USER")),
		Password:    os.Getenv("QNAP_PASSWORD"),
		InsecureTLS: envBool("QNAP_INSECURE_TLS", true),
		SID:         strings.TrimSpace(os.Getenv("QNAP_SID")),
	}
	if cfg.Scheme == "" {
		cfg.Scheme = "https"
	}
	if secs := envInt("QNAP_TIMEOUT", 30); secs > 0 {
		cfg.Timeout = time.Duration(secs) * time.Second
	}
	if cfg.Host == "" {
		return cfg, fmt.Errorf("缺少环境变量 QNAP_HOST（例如 192.168.1.10:8080）")
	}
	if cfg.SID == "" && cfg.User == "" {
		return cfg, fmt.Errorf("缺少环境变量 QNAP_USER")
	}
	if cfg.SID == "" && cfg.Password == "" {
		return cfg, fmt.Errorf("缺少环境变量 QNAP_PASSWORD")
	}
	return cfg, nil
}

func envBool(name string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Client is a QNAP File Station API client. It is safe for concurrent use.
type Client struct {
	cfg     Config
	baseURL string
	http    *http.Client
	mu      sync.Mutex
	sid     string
}

// New creates a Client from cfg.
func New(cfg Config) (*Client, error) {
	base, err := normalizeBaseURL(cfg.Host, cfg.Scheme)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureTLS}, // #nosec G402 - self-signed NAS certificates are common
	}
	return &Client{
		cfg:     cfg,
		baseURL: base,
		http:    &http.Client{Timeout: timeout, Transport: tr},
		sid:     cfg.SID,
	}, nil
}

func normalizeBaseURL(host, scheme string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("host 不能为空")
	}
	if !strings.Contains(host, "://") {
		if scheme == "" {
			scheme = "https"
		}
		host = scheme + "://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("无效的 QNAP 地址 %q: %w", host, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("QNAP 地址协议必须是 http 或 https，当前为 %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("无效的 QNAP 地址 %q：缺少主机", host)
	}
	// QTS 默认的 HTTP 管理端口是 8080.
	if u.Port() == "" && u.Scheme == "http" {
		u.Host += ":8080"
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// --- authentication -------------------------------------------------------

type authResponse struct {
	AuthPassed string `xml:"authPassed"`
	AuthSid    string `xml:"authSid"`
	IsAdmin    string `xml:"isAdmin"`
	ErrorValue string `xml:"errorValue"`
	Need2SV    string `xml:"need_2sv"`
}

func (c *Client) login(ctx context.Context) error {
	q := url.Values{}
	q.Set("user", c.cfg.User)
	// QTS 期望密码为 base64(UTF-8 password)，参考 authLogin.cgi 文档。
	q.Set("pwd", base64.StdEncoding.EncodeToString([]byte(c.cfg.Password)))
	q.Set("serviceKey", "1")
	q.Set("service", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+authLoginPath+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("构造登录请求失败: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接 QNAP 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取登录响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("登录 HTTP 状态 %d: %s", resp.StatusCode, truncate(string(body)))
	}

	var ar authResponse
	if err := xml.Unmarshal(body, &ar); err != nil {
		return fmt.Errorf("解析登录响应失败: %w (响应: %s)", err, truncate(string(body)))
	}
	if ar.Need2SV == "1" {
		return fmt.Errorf("该 QNAP 账户开启了两步验证，请在 QTS 中为该应用生成应用密码并通过 QNAP_PASSWORD 传入")
	}
	if ar.AuthPassed != "1" || ar.AuthSid == "" {
		return fmt.Errorf("QNAP 登录失败 (authPassed=%q, errorValue=%q)，请检查用户名密码", ar.AuthPassed, ar.ErrorValue)
	}
	c.mu.Lock()
	c.sid = ar.AuthSid
	c.mu.Unlock()
	return nil
}

func (c *Client) getSID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sid
}

func (c *Client) clearSID() {
	c.mu.Lock()
	c.sid = ""
	c.mu.Unlock()
}

func (c *Client) ensureSID(ctx context.Context) error {
	if c.getSID() != "" {
		return nil
	}
	return c.login(ctx)
}

// --- low level request helpers -------------------------------------------

type apiStatus struct {
	Status  *int   `json:"status"`
	Success string `json:"success"`
}

// call executes a utilRequest.cgi function and returns the raw (cleaned) JSON body.
// On a permission/status-4 response it re-authenticates once and retries.
func (c *Client) call(ctx context.Context, fn string, params url.Values) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.ensureSID(ctx); err != nil {
			return nil, err
		}
		q := cloneValues(params)
		q.Set("func", fn)
		q.Set("sid", c.getSID())
		reqURL := c.baseURL + utilRequestPath + "?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("构造请求失败: %w", err)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("调用 %s 失败: %w", fn, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取 %s 响应失败: %w", fn, readErr)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if attempt == 0 {
				c.clearSID()
				continue
			}
			return nil, fmt.Errorf("%s 返回 HTTP %d: %s", fn, resp.StatusCode, truncate(string(body)))
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s 返回 HTTP %d: %s", fn, resp.StatusCode, truncate(string(body)))
		}
		clean := trimToJSON(body)
		// status=4 通常表示会话失效或权限不足；有账号密码时刷新 sid 并重试一次。
		if attempt == 0 && statusCode(clean) == 4 {
			if c.cfg.User == "" || c.cfg.Password == "" {
				return nil, fmt.Errorf("%s: %s（QNAP_SID 可能已过期，请同时配置 QNAP_USER/QNAP_PASSWORD 以便自动续期）", fn, statusMessage(4))
			}
			c.clearSID()
			if err := c.login(ctx); err != nil {
				return nil, err
			}
			lastErr = fmt.Errorf("%s: %s", fn, statusMessage(4))
			continue
		}
		return clean, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%s 请求失败", fn)
}

func (c *Client) callJSON(ctx context.Context, fn string, params url.Values, out any) error {
	body, err := c.call(ctx, fn, params)
	if err != nil {
		return err
	}
	if err := checkStatus(body); err != nil {
		return fmt.Errorf("%s: %w", fn, err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 %s 响应失败: %w (响应: %s)", fn, err, truncate(string(body)))
	}
	return nil
}

func cloneValues(src url.Values) url.Values {
	dst := url.Values{}
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	return dst
}

func trimToJSON(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	// 某些 QTS 版本会在 JSON 前输出一行 Content-type 头。
	if i := strings.IndexAny(s, "{["); i > 0 {
		s = s[i:]
	}
	return []byte(strings.TrimSpace(s))
}

func statusCode(body []byte) int {
	var st apiStatus
	if err := json.Unmarshal(body, &st); err != nil || st.Status == nil {
		return 0
	}
	return *st.Status
}

func checkStatus(body []byte) error {
	var st apiStatus
	if err := json.Unmarshal(body, &st); err != nil {
		return nil // 交给后续 json.Unmarshal 处理
	}
	if st.Status == nil || *st.Status == 1 {
		return nil
	}
	return fmt.Errorf("操作失败 (status=%d: %s)", *st.Status, statusMessage(*st.Status))
}

func statusMessage(code int) string {
	switch code {
	case 1:
		return "成功"
	case 2:
		return "文件/文件夹已存在"
	case 3:
		return "无效参数"
	case 4:
		return "权限不足或会话已失效"
	case 5:
		return "目标不存在"
	case 9:
		return "空间配额不足"
	default:
		return "未知错误"
	}
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500] + "..."
	}
	return s
}

func bool01(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// normalizePath ensures a leading slash for QNAP paths.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if p == "share_root" || p == "recycle_root" || p == "vol_root" {
		return p
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// --- public data types ----------------------------------------------------

// TreeNode is an entry returned by get_tree (shared folders / subfolders).
type TreeNode struct {
	Text         string `json:"text"`
	ID           string `json:"id"`
	Cls          string `json:"cls"` // r = 只读, w = 读写
	IconCls      string `json:"iconCls"`
	Draggable    int    `json:"draggable"`
	NoSupportACL int    `json:"noSupportACL"`
}

// FileEntry describes a file or folder returned by get_list / stat / search.
type FileEntry struct {
	Filename  string `json:"filename"`
	IsFolder  int    `json:"isfolder"`
	FileSize  string `json:"filesize"`
	Group     string `json:"group"`
	Owner     string `json:"owner"`
	Privilege string `json:"privilege"`
	FileType  int    `json:"filetype"`
	MT        string `json:"mt"`
	EpochMT   int64  `json:"epochmt"`
}

// ListResult is the common list/stat/search response.
type ListResult struct {
	Total       int         `json:"total"`
	ACL         int         `json:"acl"`
	IsACLEnable int         `json:"is_acl_enable"`
	Datas       []FileEntry `json:"datas"`
	Page        int         `json:"page,omitempty"`
}

// OpResult is the response to mutating operations.
type OpResult struct {
	Status  int    `json:"status"`
	Success string `json:"success"`
}

// FileSizeResult is the response to get_file_size.
type FileSizeResult struct {
	Status int    `json:"status"`
	Size   string `json:"size"`
}

// --- File Station operations ---------------------------------------------

// ListShares lists all shared folders (get_tree, node=share_root).
func (c *Client) ListShares(ctx context.Context) ([]TreeNode, error) {
	return c.tree(ctx, "share_root")
}

// ListFolders lists subfolders under node (get_tree).
func (c *Client) ListFolders(ctx context.Context, node string) ([]TreeNode, error) {
	return c.tree(ctx, normalizePath(node))
}

func (c *Client) tree(ctx context.Context, node string) ([]TreeNode, error) {
	p := url.Values{}
	p.Set("is_iso", "0")
	p.Set("node", node)
	p.Set("hidden_file", "1")
	var out []TreeNode
	if err := c.callJSON(ctx, "get_tree", p, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListFilesInput are the options for ListFiles.
type ListFilesInput struct {
	Path       string
	Limit      int
	Start      int
	Sort       string
	Dir        string
	HiddenFile bool
}

// ListFiles lists files and folders in a directory (get_list).
func (c *Client) ListFiles(ctx context.Context, in ListFilesInput) (*ListResult, error) {
	p := url.Values{}
	p.Set("is_iso", "0")
	p.Set("list_mode", "all")
	p.Set("path", normalizePath(in.Path))
	p.Set("dir", orDefault(in.Dir, "ASC"))
	p.Set("sort", orDefault(in.Sort, "filename"))
	p.Set("start", strconv.Itoa(in.Start))
	p.Set("limit", strconv.Itoa(nonPositiveDefault(in.Limit, 1000)))
	p.Set("hidden_file", bool01(in.HiddenFile))
	var out ListResult
	if err := c.callJSON(ctx, "get_list", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func nonPositiveDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// Stat returns metadata for one or more files/folders in dirPath.
func (c *Client) Stat(ctx context.Context, dirPath string, names []string) (*ListResult, error) {
	p := url.Values{}
	p.Set("path", normalizePath(dirPath))
	p.Set("file_total", strconv.Itoa(len(names)))
	for _, n := range names {
		p.Add("file_name", n)
	}
	var out ListResult
	if err := c.callJSON(ctx, "stat", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Search finds files/folders by keyword under sourcePath.
func (c *Client) Search(ctx context.Context, keyword, sourcePath string, limit, start int, sort, dir string) (*ListResult, error) {
	p := url.Values{}
	p.Set("keyword", keyword)
	p.Set("source_path", normalizePath(sourcePath))
	p.Set("dir", orDefault(dir, "ASC"))
	p.Set("sort", orDefault(sort, "filename"))
	p.Set("start", strconv.Itoa(start))
	p.Set("limit", strconv.Itoa(nonPositiveDefault(limit, 1000)))
	var out ListResult
	if err := c.callJSON(ctx, "search", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateFolder creates a folder under parentPath.
func (c *Client) CreateFolder(ctx context.Context, parentPath, name string) (*OpResult, error) {
	p := url.Values{}
	p.Set("dest_folder", name)
	p.Set("dest_path", normalizePath(parentPath))
	var out OpResult
	if err := c.callJSON(ctx, "createdir", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Rename renames sourceName to destName inside dirPath.
func (c *Client) Rename(ctx context.Context, dirPath, sourceName, destName string) (*OpResult, error) {
	p := url.Values{}
	p.Set("path", normalizePath(dirPath))
	p.Set("source_name", sourceName)
	p.Set("dest_name", destName)
	var out OpResult
	if err := c.callJSON(ctx, "rename", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes files/folders from dirPath.
func (c *Client) Delete(ctx context.Context, dirPath string, names []string) (*OpResult, error) {
	p := url.Values{}
	p.Set("path", normalizePath(dirPath))
	p.Set("file_total", strconv.Itoa(len(names)))
	for _, n := range names {
		p.Add("file_name", n)
	}
	var out OpResult
	if err := c.callJSON(ctx, "delete", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Copy copies files/folders from sourcePath to destPath.
// mode: 0 = overwrite, 1 = skip. dup is the duplicate suffix (e.g. "copy").
func (c *Client) Copy(ctx context.Context, sourcePath, destPath string, names []string, mode int, dup string) (*OpResult, error) {
	return c.transfer(ctx, "copy", sourcePath, destPath, names, mode, dup)
}

// Move moves files/folders from sourcePath to destPath. mode: 0 overwrite, 1 skip.
func (c *Client) Move(ctx context.Context, sourcePath, destPath string, names []string, mode int) (*OpResult, error) {
	return c.transfer(ctx, "move", sourcePath, destPath, names, mode, "")
}

func (c *Client) transfer(ctx context.Context, fn, sourcePath, destPath string, names []string, mode int, dup string) (*OpResult, error) {
	p := url.Values{}
	for _, n := range names {
		p.Add("source_file", n)
	}
	p.Set("source_total", strconv.Itoa(len(names)))
	p.Set("source_path", normalizePath(sourcePath))
	p.Set("dest_path", normalizePath(destPath))
	p.Set("mode", strconv.Itoa(mode))
	if dup != "" {
		p.Set("dup", dup)
	}
	var out OpResult
	if err := c.callJSON(ctx, fn, p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetFileSize returns the total size of the given files/folders in dirPath.
func (c *Client) GetFileSize(ctx context.Context, dirPath string, names []string) (*FileSizeResult, error) {
	p := url.Values{}
	p.Set("path", normalizePath(dirPath))
	p.Set("total", strconv.Itoa(len(names)))
	for _, n := range names {
		p.Add("name", n)
	}
	var out FileSizeResult
	if err := c.callJSON(ctx, "get_file_size", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadTo streams a download response into w and returns the suggested
// filename from Content-Disposition, if present.
func (c *Client) DownloadTo(ctx context.Context, w io.Writer, dirPath string, names []string, isFolder bool, compress int) (string, error) {
	if err := c.ensureSID(ctx); err != nil {
		return "", err
	}
	p := url.Values{}
	p.Set("isfolder", bool01(isFolder))
	p.Set("compress", strconv.Itoa(compress))
	p.Set("source_path", normalizePath(dirPath))
	p.Set("source_total", strconv.Itoa(len(names)))
	for _, n := range names {
		p.Add("source_file", n)
	}
	p.Set("func", "download")
	p.Set("sid", c.getSID())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+utilRequestPath+"?"+p.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("构造下载请求失败: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("下载返回 HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return "", fmt.Errorf("写入下载内容失败: %w", err)
	}
	return suggestedFilename(resp.Header.Get("Content-Disposition")), nil
}

func suggestedFilename(cd string) string {
	if cd == "" {
		return ""
	}
	for _, part := range strings.Split(cd, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "filename=") {
			name := strings.Trim(strings.TrimPrefix(part, "filename="), `"`)
			return filepath.Base(name)
		}
		if strings.HasPrefix(part, "filename*=") {
			name := strings.Trim(strings.TrimPrefix(part, "filename*="), `"`)
			if i := strings.LastIndex(name, "''"); i >= 0 {
				name = name[i+2:]
			}
			return filepath.Base(name)
		}
	}
	return ""
}

// Upload uploads a local file into destPath via multipart form.
func (c *Client) Upload(ctx context.Context, localPath, destPath string, overwrite bool) (*OpResult, error) {
	if err := c.ensureSID(ctx); err != nil {
		return nil, err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取本地文件信息失败: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("本地路径是文件夹，upload 仅支持单个文件")
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return nil, fmt.Errorf("构造上传表单失败: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, fmt.Errorf("读取本地文件内容失败: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("结束上传表单失败: %w", err)
	}

	fileName := filepath.Base(localPath)
	progress := strings.ReplaceAll(normalizePath(destPath)+"/"+fileName, "/", "-")
	q := url.Values{}
	q.Set("func", "upload")
	q.Set("type", "standard")
	q.Set("sid", c.getSID())
	q.Set("dest_path", normalizePath(destPath))
	q.Set("overwrite", bool01(overwrite))
	q.Set("progress", progress)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+utilRequestPath+"?"+q.Encode(), &buf)
	if err != nil {
		return nil, fmt.Errorf("构造上传请求失败: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上传失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("读取上传响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上传返回 HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}
	clean := trimToJSON(body)
	if err := checkStatus(clean); err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	var out OpResult
	if err := json.Unmarshal(clean, &out); err != nil {
		return nil, fmt.Errorf("解析上传响应失败: %w (响应: %s)", err, truncate(string(clean)))
	}
	return &out, nil
}
