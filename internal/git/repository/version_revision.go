package gitrepo

import (
	"context"
	"errors"
	"strings"
	"time"

	"release-desk/internal/platform/process"
	"release-desk/internal/policy"
)

// VersionRevision 固定指定发布分支的代码来源，不切换或合并工作区。
type VersionRevision struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// VersionRevision 只读指定版本在本地引用中最新的已知提交。
// 参数 ctx：本次扫描的取消上下文，停止任务时同时停止 Git 子进程。
// 参数 version：软件中的版本名，精确对应 release-<version> 分支。
// 返回：固定引用与 SHA；分支不存在、远端歧义或读取失败时返回错误，不回退 HEAD。
func (g *Git) VersionRevision(ctx context.Context, version string) (VersionRevision, error) {
	// 版本内容不能成为 Git 选项、通配符或跨版本的 refspec。
	branch := "release-" + version
	if strings.TrimSpace(version) == "" || strings.ContainsAny(version, ":*?[]\\ \t\r\n") {
		return VersionRevision{}, errors.New("发布版本名无效")
	}
	// Git 再核对完整分支语法，不能让特殊引用表达式逃逸到其他对象。
	if _, err := g.runVersionGit(ctx, "check-ref-format", "--branch", branch); err != nil {
		return VersionRevision{}, errors.New("发布分支名无效")
	}
	// 只枚举已配置的跟踪引用；优先 origin，多远端无主选时拒绝猜测。
	refs, err := g.runVersionGit(ctx, "for-each-ref", "--format=%(refname)", "refs/remotes/")
	// 引用枚举失败不能被误解成只有本地分支。
	if err != nil {
		return VersionRevision{}, err
	}
	// 多远端选择保持确定性，不能把同名分支误认成唯一来源。
	remote, err := versionRemote(strings.Fields(refs), branch)
	if err != nil {
		return VersionRevision{}, err
	}
	// 没有远端时仅允许明确的本地发布分支，绝不自动借用当前 HEAD。
	ref := "refs/heads/" + branch
	if remote != "" {
		// 远端跟踪分支也是本地已有引用，只读它的对象，不发起网络请求。
		ref = "refs/remotes/" + remote + "/" + branch
		// 本地发布分支包含远端且有新提交时保留本地进度，分叉则以远端为准。
		if _, localErr := g.runVersionGit(ctx, "merge-base", "--is-ancestor", ref, "refs/heads/"+branch); localErr == nil {
			ref = "refs/heads/" + branch
		}
	}
	// 将引用解析为不可变提交对象，后台分析不再追随后续分支移动。
	sha, err := g.runVersionGit(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return VersionRevision{}, errors.New("无法读取该版本发布分支，请检查仓库及版本名称")
	}
	// SHA 与引用一起供模型和来源记录使用，不能把拉取成功等同于已上线。
	return VersionRevision{Ref: ref, SHA: strings.TrimSpace(sha)}, nil
}

// versionRemote 为精确版本选择唯一远端，避免误取另一发布分支。
// 参数 refs：Git 返回的完整远端引用列表；参数 branch：已校验的精确发布分支名。
// 返回：远端名，无跟踪引用时为空；多个非 origin 候选时返回错误。
func versionRemote(refs []string, branch string) (string, error) {
	// 保留所有精确候选，缺少默认 origin 时不能随便取枚举第一项。
	var found []string
	for _, ref := range refs {
		// 用完整分支后缀匹配，5.7.3 不能误命中 5.7.30。
		if !strings.HasSuffix(ref, "/"+branch) {
			continue
		}
		// 去除的是完整发布分支后缀，远端名称中的路径字符保持原样。
		remote := strings.TrimSuffix(strings.TrimPrefix(ref, "refs/remotes/"), "/"+branch)
		// 项目通常使用 origin，它存在时优先于其他镜像远端。
		if remote == "origin" {
			return remote, nil
		}
		found = append(found, remote)
	}
	// 两个非默认远端可能不是同一份代码，需要用户先明确来源。
	if len(found) > 1 {
		return "", errors.New("该版本存在多个远端，请保留明确的 origin 跟踪引用后重试")
	}
	// 没有远端跟踪引用时，调用方仅尝试本地发布分支。
	if len(found) == 0 {
		return "", nil
	}
	return found[0], nil
}

// runVersionGit 执行一次有界、可取消的本地引用读取，不泄露仓库内部输出。
// 参数 parent：单项更新的取消上下文；参数 args：独立 Git 参数，不经 shell 解释。
// 返回：成功输出或脱敏错误；调用方不能将失败当成空引用继续扫描。
func (g *Git) runVersionGit(parent context.Context, args ...string) (string, error) {
	// 配置读取锁只覆盖这一条命令，来源路径不会在命令执行中切换。
	g.configMu.RLock()
	defer g.configMu.RUnlock()
	// 本地 Git 异常等待也有上限，用户停止优先于完整超时时间。
	ctx, cancel := context.WithTimeout(parent, policy.GitTimeoutSeconds*time.Second)
	defer cancel()
	// 参数数组直接传进程，版本名和目录不会经过命令行二次解释。
	cmd := process.Command(ctx, "git", append([]string{"-C", g.path}, args...)...)
	// Git 错误输出可能含 URL 或凭据，不直接进入软件提示或日志。
	output, err := cmd.Output()
	// 保留取消语义，扫描收尾可据此显示已停止而非工具故障。
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", errors.New("Git 版本引用读取或刷新失败，请检查远端连接及分支权限")
	}
	return strings.TrimSpace(string(output)), nil
}
