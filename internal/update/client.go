package updateservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"release-desk/internal/policy"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)
var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Size  int64  `json:"size"`
		URL   string `json:"browser_download_url"`
	} `json:"assets"`
}

// fetch 读取最新稳定发布，地址只由内嵌仓库标识构成。
// 参数 ctx：退出或请求超时上下文，不允许用业务记录指定任意主机。
// 返回：可安全展示的发布信息；限流、协议、体积或版本异常时返回错误。
func (s *Service) fetch(ctx context.Context) (Status, error) {
	result := Status{Current: s.current}
	// 来源只允许 owner/repository 两段，禁止 URL、参数或额外路径作为仓库名。
	if !repositoryPattern.MatchString(s.repository) {
		return result, errors.New("更新仓库标识无效")
	}
	// 公共 API 使用匿名请求，不读取浏览器、Git、Codex 或数据库凭据。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+s.repository+"/releases/latest", nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// 固定官方支持的响应协议，版本号和 UA 不包含任何用户业务数据。
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ReleaseDesk/"+s.current)
	// HTTP 客户端具有集中超时，断网不能无限占用后台检查。
	response, err := s.client.Do(req)
	// 连接故障向更新面板报告，不能回退成测试数据库或其他网络地址。
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	// 每条分支都关闭响应体，长期运行不能因定期检查累积连接资源。
	// 新建仓库尚无 Release 是正常情况，不能显示成已确认最新版或程序故障。
	if response.StatusCode == http.StatusNotFound {
		result.Note = "仓库暂未发布正式安装包"
		return result, nil
	}
	if response.StatusCode != http.StatusOK {
		// 限流和服务异常保留错误语义，不解析其正文或伪装成无新版。
		return result, fmt.Errorf("GitHub 更新请求返回 HTTP %d", response.StatusCode)
	}
	// 响应正文包含远端文字，先限制体积再解析，不把错误 HTML 当作发布信息。
	content, err := io.ReadAll(io.LimitReader(response.Body, policy.MaxUpdateResponseBytes+1))
	if err != nil || len(content) > policy.MaxUpdateResponseBytes {
		return result, errors.New("GitHub 更新响应读取失败或超过大小上限")
	}
	var release githubRelease
	// 仅解码发布所需字段，不保存作者资料、下载统计或远端附带的其他信息。
	if err := json.Unmarshal(content, &release); err != nil {
		return result, errors.New("GitHub 发布信息格式无效")
	}
	// 正式更新不能自动选择草稿或预发布；旧稳定版也不能作为降级候选。
	if release.Draft || release.Prerelease {
		result.Note = "没有可用的正式更新"
		return result, nil
	}
	// 标签必须是明确的软件语义版本，正式更新不依据发布时间强制降级。
	newer, err := newerVersion(release.Tag, s.current)
	if err != nil {
		return result, err
	}
	result.Latest, result.Note = strings.TrimPrefix(release.Tag, "v"), release.Body
	// 发布页地址由已验证仓库和标签生成，不采用响应里的任意外部地址。
	result.ReleaseURL = "https://github.com/" + s.repository + "/releases/tag/" + release.Tag
	if !newer {
		// 相同版本的重复打包只保留说明，不要求用户替换同版本 EXE。
		return result, nil
	}
	// 文件名、仓库、标签和 HTTPS 地址必须全部匹配，不允许响应将下载引向其他站点。
	name := "ReleaseDesk-" + result.Latest + ".exe"
	expectedURL := "https://github.com/" + s.repository + "/releases/download/" + release.Tag + "/" + name
	for _, asset := range release.Assets {
		// 只选择 Windows 桌面 EXE，ZIP、源码和尚未上传完的资源不触发升级提示。
		if asset.Name == name && asset.State == "uploaded" && asset.Size > 0 && asset.URL == expectedURL {
			// 资源已完整上传才显示可下载，避免先打 tag 而附件尚未到位的窗口。
			result.Available, result.DownloadURL = true, expectedURL
			return result, nil
		}
	}
	// 可以查看发布页，但不能把缺失或跨站附件标记为可直接更新。
	result.Note = "发现较新发布，但尚无可用的 Windows EXE。\n" + result.Note
	return result, nil
}

// newerVersion 严格按三段数值比较软件版本，不使用字符串或业务发布版本排序。
// 参数 candidate：GitHub 标签，允许 v 前缀；参数 current：当前软件 x.y.z。
// 返回：仅 candidate 更高时为 true；不规范或数值溢出时返回错误。
func newerVersion(candidate, current string) (bool, error) {
	// 预发布和不明确的标签不能进入自动更新，避免把业务 5.x 清单或任意名字当作程序版本。
	left, right := versionPattern.FindStringSubmatch(candidate), versionPattern.FindStringSubmatch(current)
	if left == nil || right == nil {
		return false, errors.New("更新版本号格式无效")
	}
	newer, decided := false, false
	// v 仅是标签前缀，去除后数字段与当前软件版本采用相同规则。
	// 逐段数值比较，1.10.0 必须高于 1.9.0，不能使用字典顺序。
	for index := 1; index <= 3; index++ {
		// 每一段都校验范围，即使前段已经分出高低也不能接受后段溢出的非法标签。
		a, ae := strconv.ParseUint(left[index], 10, 64)
		b, be := strconv.ParseUint(right[index], 10, 64)
		if ae != nil || be != nil {
			return false, errors.New("更新版本号超出范围")
		}
		if !decided && a != b {
			// 首个不同数字段决定高低，其余段仍继续完成格式和范围核验。
			newer, decided = a > b, true
		}
	}
	// 版本完全相同表示同版构建，不通过 BuildID 自动覆盖用户正在使用的程序。
	return newer, nil
}
