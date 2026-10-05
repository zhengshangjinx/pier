// Package update 是自更新那条链路：查最新版本、挑产物、下载、校验、解压，
// 以及（见 apply_*.go）在 Pier 退出之后把文件换掉。
//
// 界面与命令行共用这里的一切：检查与下载只有一份实现，两边看到的进度与失败原因
// 才是同一句话。网络与目录都可以注入（Options），测试一律用 httptest 顶掉，
// 不碰真实网络——这条规矩仓库里一直没有破过。
package update

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/version"
)

const (
	// DefaultAPI 是 GitHub 的公开接口地址。
	DefaultAPI = "https://api.github.com"

	// Repo 是发布产物所在的地方。
	Repo = "zhengshangjinx/pier"

	// StateName 是「上次查到哪儿了」的文件名，和 services.json 同在数据目录里。
	StateName = "update.json"

	// baseURLEnv 可以换掉 API 基址。只给手工验收用：让真界面连一个本地的假
	// release 走完整条路——不发一版就试不了这个功能，除此之外没有别的办法。
	baseURLEnv = "PIER_UPDATE_BASE_URL"

	// apiTimeout 是查一次最新版本的时限。它就是个 GET，卡这么久不必再等。
	// 下载不走这个时限（几十兆的产物，见 attemptTimeout）。
	apiTimeout = 30 * time.Second

	// maxReleaseBytes 是一份 release 响应的上限。发布说明写得再长也就几十 KB，
	// 这个数挡的是「基址被顶掉之后有人塞回来一份没完没了的响应」。
	maxReleaseBytes = 1 << 20

	// sumsName 是产物旁边那份校验和文件的名字，由 package.sh 生成。
	sumsName = "SHA256SUMS"

	// maxSumsBytes 是校验和文件的上限，理由同上：它只有几百字节。
	maxSumsBytes = 1 << 20
)

// Options 是 Client 的可注入项。零值就是照常干活：官方地址、默认网络、
// 数据目录下的 cache/update、当前这份二进制的版本号。
type Options struct {
	// BaseURL 是 API 基址，空表示官方地址；PIER_UPDATE_BASE_URL 也能顶掉它。
	BaseURL string
	// HTTP 是发请求用的客户端，空表示默认的那个。
	HTTP *http.Client
	// Dir 是下载与解压的落脚处，空表示数据目录下的 cache/update。
	Dir string
	// Repo 是 owner/name，空表示 Pier 自己的仓库。
	Repo string
	// Current 是当前版本号，空表示 version.Current()。
	Current string
}

// Client 握着这条路线上唯一的一点状态：上次查到哪儿了、往哪儿下。
// 一个进程建一个就够；方法可以并发调用（后台那次检查与界面上的按钮会同时进来）。
type Client struct {
	base    string
	http    *http.Client
	dir     string
	repo    string
	current string
	ua      string

	mu    sync.Mutex
	state State
}

// New 建一个 Client，顺手把上次的状态读回来。读不出来（文件不在、读坏了）就当没有，
// 那只不过意味着这次要完整地查一遍。
func New(opts Options) *Client {
	c := &Client{
		base:    strings.TrimRight(firstNonEmpty(opts.BaseURL, os.Getenv(baseURLEnv), DefaultAPI), "/"),
		http:    opts.HTTP,
		dir:     opts.Dir,
		repo:    firstNonEmpty(opts.Repo, Repo),
		current: firstNonEmpty(opts.Current, version.Current()),
	}
	if c.http == nil {
		// 用默认 transport：它会认 HTTPS_PROXY 这一组环境变量——直连 api.github.com
		// 常常不通，系统代理是用户手里唯一那把钥匙。
		// **不设 Client.Timeout**：那个时限管的是整个请求，几十兆的下载会被它一刀切掉。
		c.http = &http.Client{}
	}
	// User-Agent 必须带：GitHub 对没有它的请求直接 403。
	c.ua = "Pier/" + c.current
	if c.dir == "" {
		if d, err := config.Dirs(); err == nil {
			c.dir = filepath.Join(d.Cache, "update")
		}
	}
	if p, err := StatePath(); err == nil {
		c.setState(LoadState(p))
	}
	return c
}

// Current 返回当前这份二进制的版本号。
func (c *Client) Current() string { return c.current }

// Dir 返回下载与解压落脚的那个目录。
func (c *Client) Dir() string { return c.dir }

// State 返回上次查到的那一份。
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Client) setState(s State) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

// State 是「上次查到哪儿了」，落在数据目录的 update.json 里。
//
// 这不是偏好（偏好是 settings.json，用户改的），写坏了只是下次重查一遍，
// 所以读不出来就当没有。存下来有两个用处：带着 ETag 做条件请求（命中 304
// 不计入限流），以及重启之后不必再查一次就能接着下载上次看到的那一版——
// 里面的资产地址是可以直接用的。
type State struct {
	// CheckedAt 是上次真的问到服务端的时刻（304 也算问到了）。
	CheckedAt time.Time `json:"checkedAt"`
	// ETag 是上次那份 release 的标记，下次带着它做条件请求。
	ETag string `json:"etag,omitempty"`
	// Release 是上次查到的那一版。
	Release Release `json:"release"`
}

// StatePath 返回状态文件的默认位置。
func StatePath() (string, error) {
	d, err := config.Dirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Data, StateName), nil
}

// LoadState 读状态。文件不在、读不动、读坏了都返回空状态，不算错误：
// 它只是「上次查到哪儿」，不是用户写的配置。
func LoadState(path string) State {
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}
	}
	return s
}

// SaveState 写状态，走和数据目录里别的文件一样的原子写。
func SaveState(path string, s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteAtomic(path, append(raw, '\n'))
}

// ReleasesPage 是发布页地址。「打开下载页」「查看发布说明」开的就是它——
// 地址只在这儿拼一份：命令行与界面各写一遍，两处迟早指向不同的地方。
func ReleasesPage() string { return "https://github.com/" + Repo + "/releases" }

// firstNonEmpty 返回第一个非空串。配置项都是「空表示走默认」。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
