package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testAsset 是拼一份假 release 响应时用的一份产物。
type testAsset struct {
	name   string
	url    string
	size   int64
	digest string
}

// releaseBody 拼一份 GitHub 的 release 响应。字段名按官方文档写，
// 免得测试跟着实现一起错。
func releaseBody(tag, notes string, assets ...testAsset) []byte {
	as := make([]map[string]any, 0, len(assets))
	for _, a := range assets {
		m := map[string]any{"name": a.name, "browser_download_url": a.url, "size": a.size}
		if a.digest != "" {
			m["digest"] = a.digest
		}
		as = append(as, m)
	}
	raw, err := json.Marshal(map[string]any{
		"tag_name":     tag,
		"body":         notes,
		"html_url":     "https://github.com/zhengshangjinx/pier/releases/tag/" + tag,
		"published_at": "2026-10-01T08:00:00Z",
		"assets":       as,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// githubStub 起一个假的 GitHub。handler 能看见每一次请求，
// 测试据此断言「带了什么头」「发了几次」。
func githubStub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckFindsUpdate(t *testing.T) {
	var gotUA, gotPath, gotAccept string
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotPath, gotAccept = r.Header.Get("User-Agent"), r.URL.Path, r.Header.Get("Accept")
		w.Header().Set("ETag", `W/"v1"`)
		w.Write(releaseBody("v0.3.0", "修了几个问题",
			testAsset{name: "Pier-0.3.0-macos-universal.zip", url: "http://x/a.zip", size: 100},
			testAsset{name: "SHA256SUMS", url: "http://x/SHA256SUMS", size: 200},
		))
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Release.Version != "0.3.0" || res.Release.Tag != "v0.3.0" {
		t.Errorf("解析出来的版本不对：%+v", res.Release)
	}
	if res.Release.Notes != "修了几个问题" {
		t.Errorf("发布说明没读到：%q", res.Release.Notes)
	}
	if res.Release.Published.Year() != 2026 {
		t.Errorf("发布时间没读到：%v", res.Release.Published)
	}
	if len(res.Release.Assets) != 2 || res.Release.Assets[0].Name != "Pier-0.3.0-macos-universal.zip" {
		t.Errorf("产物没解析对：%+v", res.Release.Assets)
	}
	if !res.HasUpdate {
		t.Error("0.3.0 比 0.2.0 新，该说有新版")
	}
	if res.NotModified {
		t.Error("这是 200，不该标成 304")
	}
	// 端点与请求头都是 GitHub 的硬要求，写错一个就是永远查不到。
	if gotPath != "/repos/zhengshangjinx/pier/releases/latest" {
		t.Errorf("请求的路径是 %q", gotPath)
	}
	if gotUA == "" {
		t.Error("没带 User-Agent——GitHub 对没有它的请求直接 403")
	}
	if !strings.Contains(gotAccept, "github+json") {
		t.Errorf("Accept 头是 %q", gotAccept)
	}
}

// 命中 304 时不该丢东西：上次那一版就存在状态里，照样能说清楚「有没有新版」，
// 而这一版的信息（发布说明、产物地址）也还在。
func TestCheckNotModified(t *testing.T) {
	var sawCond string
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "" {
			w.Header().Set("ETag", `W/"v1"`)
			w.Write(releaseBody("v0.3.0", "说明"))
			return
		}
		sawCond = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	})

	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 换一个 Client——这就是「关了再开」：状态是从磁盘读回来的。
	c2 := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	res, err := c2.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sawCond != `W/"v1"` {
		t.Errorf("第二次没带上 ETag，服务端看见的是 %q", sawCond)
	}
	if !res.NotModified {
		t.Error("服务端回的 304，该标成未修改")
	}
	if !res.HasUpdate || res.Release.Version != "0.3.0" {
		t.Errorf("304 之后该用上次那一版接着判断，得到 %+v", res)
	}
}

func TestCheckErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    []byte
		want    string
	}{
		{
			name:    "限流",
			status:  http.StatusForbidden,
			headers: map[string]string{"X-RateLimit-Remaining": "0"},
			want:    "访问次数用完了",
		},
		{
			name:   "被拒",
			status: http.StatusForbidden,
			want:   "拒绝了",
		},
		{
			name:   "仓库或发布不存在",
			status: http.StatusNotFound,
			want:   "没有这个仓库",
		},
		{
			name:   "读不懂",
			status: http.StatusOK,
			body:   []byte("<html>网关</html>"),
			want:   "读不懂",
		},
		{
			name:   "预发布版不认",
			status: http.StatusOK,
			body:   []byte(`{"tag_name":"v0.4.0-rc1","prerelease":true}`),
			want:   "草稿或预发布版",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				if c.body != nil {
					w.Write(c.body)
				}
			})
			cl := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
			_, err := cl.Check(context.Background())
			if err == nil {
				t.Fatal("该报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误是 %q，想看到 %q", err, c.want)
			}
		})
	}
}

// tag 认不出来时版本号留空：空的版本号不参与比较，于是永远不会被判成「有新版」。
// 宁可漏报，也不能拿一个认不出的东西去催人升级。
func TestCheckUnparseableTag(t *testing.T) {
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(releaseBody("nightly", "随手打的标签"))
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Release.Version != "" {
		t.Errorf("nightly 不该被当成版本号，得到 %q", res.Release.Version)
	}
	if res.HasUpdate {
		t.Error("版本号认不出来时不该说有新版")
	}
	// tag 还是留着的：界面上要能说清楚「最新那个标签是什么」。
	if res.Release.Tag != "nightly" {
		t.Errorf("tag 该原样留着，得到 %q", res.Release.Tag)
	}
}

// 当前版本是 dev（自己编的）时不报有新版——这条是自更新的第一道保险。
func TestCheckDevCurrent(t *testing.T) {
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(releaseBody("v0.3.0", ""))
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client(), Current: "dev"})
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HasUpdate {
		t.Error("当前是 dev 时不该说有新版")
	}
}

// 查到的结果要落盘：关掉再开不该重新查一遍才知道有新版。
func TestCheckPersistsState(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"v2"`)
		w.Write(releaseBody("v0.3.0", "说明"))
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}

	path, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("状态文件没落盘：%v", err)
	}
	// 落盘的位置就该在数据目录里，和 services.json 一处。
	if filepath.Base(path) != StateName {
		t.Errorf("状态文件名是 %s，想要 %s", filepath.Base(path), StateName)
	}
	c2 := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	if got := c2.State(); got.ETag != `W/"v2"` || got.Release.Version != "0.3.0" {
		t.Errorf("重启后读回来的是 %+v", got)
	}
	if c2.State().CheckedAt.IsZero() {
		t.Error("检查时刻没落盘")
	}
}

func TestReleaseFind(t *testing.T) {
	r := Release{Assets: []Asset{{Name: "a"}, {Name: "b"}}}
	if a, ok := r.Find("b"); !ok || a.Name != "b" {
		t.Error("该找到 b")
	}
	if _, ok := r.Find("c"); ok {
		t.Error("c 不在里面")
	}
}

func TestHTTPErrorWording(t *testing.T) {
	if got := httpError(&http.Response{StatusCode: 500, Header: http.Header{}}); !strings.Contains(got.Error(), "500") {
		t.Errorf("500 的说法是 %q", got)
	}
}

// 状态文件里那份 release 要能原样写出去再读回来：时间字段的编解码最容易在这里掉链子。
func TestReleaseJSONRoundTrip(t *testing.T) {
	in := Release{
		Version:   "0.3.0",
		Tag:       "v0.3.0",
		Published: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
		Assets:    []Asset{{Name: "a.zip", URL: "http://x/a.zip", Size: 12, Digest: "sha256:ab"}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Release
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Published.Equal(in.Published) || out.Assets[0].Digest != "sha256:ab" {
		t.Errorf("转一圈回来变了：%+v", out)
	}
}
