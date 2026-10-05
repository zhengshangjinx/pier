package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zhengshangjinx/pier/internal/version"
)

// Release 是一次发布。只挑这里用得上的几样，GitHub 那份响应里的其余字段一概不看。
type Release struct {
	// Version 是规范化之后的版本号（0.3.0）。tag 认不出来时为空——
	// 空的版本号不参与比较，也就永远不会被判成「有新版」。
	Version string `json:"version,omitempty"`
	// Tag 是原始 tag，形如 v0.3.0。
	Tag string `json:"tag,omitempty"`
	// Notes 是发布说明正文（markdown）。
	Notes string `json:"notes,omitempty"`
	// URL 是发布页地址，「查看发布说明」开的就是它。
	URL string `json:"url,omitempty"`
	// Published 是发布时间，零值表示响应里没给。
	Published time.Time `json:"publishedAt,omitempty"`
	// Assets 是这一版挂上去的产物。
	Assets []Asset `json:"assets,omitempty"`
}

// Asset 是一份产物。
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
	// Digest 是 GitHub 自己算好的校验和（sha256:<hex>）。这个字段比较新，
	// 空着表示没给，那就退回下载 SHA256SUMS 那条路。
	Digest string `json:"digest,omitempty"`
}

// Find 找出名字为 name 的那份产物。
func (r Release) Find(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// displayVersion 是报错时指代这一版的说法。版本号认不出来时退回 tag，
// 好歹让人知道说的是哪一版。
func (r Release) displayVersion() string {
	if r.Version != "" {
		return "第 " + r.Version + " 版"
	}
	if r.Tag != "" {
		return "标签 " + r.Tag + " 那一版"
	}
	return "这一版"
}

// Result 是一次检查的结果。
type Result struct {
	// Release 是最新的一版。
	Release Release
	// Current 是当前这份二进制的版本号。
	Current string
	// HasUpdate 报告有没有比当前更新的版本。当前版本认不出来（dev、伪版本）时为假：
	// 宁可漏报，也不能拿一个编不出来的号去催人升级。
	HasUpdate bool
	// NotModified 表示服务端回了 304——还是上次查到的那一版，Release 取自上次的状态。
	NotModified bool
	// CheckedAt 是这次检查的时刻。
	CheckedAt time.Time
}

// Check 查一次最新版本，并把结果落盘。
//
// 带上次的 ETag 做条件请求：命中 304 时 GitHub 不计入限流，也省一次解析。
// 未认证访问的额度是每 IP 每小时 60 次，按 6 小时一查远远够；真正要防的是
// 共用出口 IP（公司 NAT）那种情况，所以限流只回报一句，不重试、不打扰。
func (c *Client) Check(ctx context.Context) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	prev := c.State()
	// 这个端点天然排除草稿与预发布版，正是我们要的「最新一版正式版」。
	api := c.base + "/repos/" + c.repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return Result{}, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if prev.ETag != "" {
		req.Header.Set("If-None-Match", prev.ETag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("连不上 %s：%w", c.base, err)
	}
	defer resp.Body.Close()

	notModified := resp.StatusCode == http.StatusNotModified
	etag := prev.ETag
	var rel Release
	switch {
	case notModified:
		rel = prev.Release
	case resp.StatusCode == http.StatusOK:
		var rj releaseJSON
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBytes)).Decode(&rj); err != nil {
			return Result{}, fmt.Errorf("读不懂 GitHub 的回应：%w", err)
		}
		if rel, err = rj.release(); err != nil {
			return Result{}, err
		}
		etag = resp.Header.Get("ETag")
	default:
		return Result{}, httpError(resp)
	}

	res := Result{
		Release:     rel,
		Current:     c.current,
		NotModified: notModified,
		CheckedAt:   time.Now(),
	}
	res.HasUpdate = version.Newer(rel.Version, c.current)

	st := State{CheckedAt: res.CheckedAt, ETag: etag, Release: rel}
	c.setState(st)
	// 状态写不进去不该让这次检查失败：它只是「上次查到哪儿」，
	// 下次到点重查一遍就有了，而这次的结果就在手上。
	if p, err := StatePath(); err == nil {
		_ = SaveState(p, st)
	}
	return res, nil
}

// releaseJSON 是 GitHub 那份响应里我们用得上的字段。
type releaseJSON struct {
	Tag        string      `json:"tag_name"`
	Body       string      `json:"body"`
	HTMLURL    string      `json:"html_url"`
	Published  *time.Time  `json:"published_at"`
	Draft      bool        `json:"draft"`
	Prerelease bool        `json:"prerelease"`
	Assets     []assetJSON `json:"assets"`
}

type assetJSON struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// release 把响应变成我们认的那一份。
//
// 草稿与预发布版再挡一道：releases/latest 本来就不会给出来，但基址是可以被顶掉的，
// 手工验收那台假服务也得走同一个判断，免得「发布说明里写的是预发布」这种事漏过去。
func (rj releaseJSON) release() (Release, error) {
	if rj.Draft || rj.Prerelease {
		return Release{}, errors.New("拿到的是草稿或预发布版，跳过")
	}
	rel := Release{Tag: rj.Tag, Notes: rj.Body, URL: rj.HTMLURL}
	if v, ok := version.Canon(rj.Tag); ok {
		rel.Version = v
	}
	if rj.Published != nil {
		rel.Published = *rj.Published
	}
	for _, a := range rj.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size, Digest: a.Digest})
	}
	return rel, nil
}

// httpError 把状态码翻成人话。限流单列一句：它最可能发生，而且用户什么都做不了，
// 过一会儿自己就好。
func httpError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return errors.New("GitHub 的接口访问次数用完了（未登录时每 IP 每小时 60 次），过一会儿再试")
		}
		return fmt.Errorf("GitHub 拒绝了这次请求（HTTP %d）", resp.StatusCode)
	case http.StatusNotFound:
		return errors.New("GitHub 上没有这个仓库，或者它还没有发布过")
	default:
		return fmt.Errorf("GitHub 返回了 HTTP %d", resp.StatusCode)
	}
}
