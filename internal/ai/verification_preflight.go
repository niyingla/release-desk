package aiservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"release-desk/internal/platform/process"
	"release-desk/internal/policy"
	"release-desk/internal/storage/workspace"
)

// verificationProbe 合并并发预检；只缓存传输可用性，不表示凭据有效或业务通过。
type verificationProbe struct {
	key  string
	done chan struct{}
	at   time.Time
	err  error
}

// ProbeLogNetwork 由隔离子进程检查精确主机的 HTTPS 传输，不读取任何凭据或响应正文。
// 参数 domain：设置中经过校验的单个日志主机；拒绝 URL、通配符及附加端口。
// 返回：收到任意 HTTP 响应表示传输可达；DNS、代理、TLS 或超时错误返回明确失败。
func ProbeLogNetwork(domain string) error {
	// 同一校验也用于 CLI 参数，探测入口不能绕过域名约束访问其他协议。
	if err := workspace.ValidateLogDomains([]string{domain}); err != nil {
		return err
	}
	client := &http.Client{Timeout: policy.VerificationProbeSeconds * time.Second,
		// 禁止跟随重定向，受检主机不能把探测转移到其他站点。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	// 不携带鉴权请求头，预检只建立 TLS 连接，不提交搜索或读取日志正文。
	response, err := client.Get("https://" + domain + "/")
	if err != nil {
		return errors.New("日志网络不可达，请检查代理、域名权限和网络连接")
	}
	// 403 等鉴权响应也说明网络已通；此处不能假装已经验证 AK/SK 或日志查询权限。
	response.Body.Close()
	return nil
}

// ensureVerificationAccess 让同批并行验证共享短期预检，故障时不先启动收费模型。
// 参数 ctx：单项取消上下文；等待共享预检也可及时停止。
// 参数 key：CLI 和允许主机组成的配置标识；配置改变必须重新检查。
// 参数 run：本轮传输检查；执行器只能做只读、有限时的网络访问。
// 返回：传输已达返回 nil；取消和检查错误原样保留，不判定业务失败。
func (s *Service) ensureVerificationAccess(ctx context.Context, key string, run func(context.Context) error) error {
	// 缓存只在本 Service 内共享，不写入真实任务历史或用户版本文件。
	s.probeMu.Lock()
	// 锁内固定条目指针；后续配置切换不会改变已经等待该条目的工作者。
	probe := s.probe
	// 短期正负结果均共享，避免六个工作者同时重复探测失败的网络。
	if probe == nil || probe.key != key || time.Since(probe.at) > policy.VerificationProbeCacheSeconds*time.Second {
		probe = &verificationProbe{key: key, done: make(chan struct{}), at: time.Now()}
		// 先公布尚未完成的条目，再释放锁；其他工作者等待而非重复执行。
		s.probe = probe
		s.probeMu.Unlock()
		// 执行过程中不持工作区锁或预检锁，页面和其他项取消仍能响应。
		probe.err = run(ctx)
		// 一项被取消不应让其他独立项复用取消结果；等待者可重新进行自己的预检。
		if errors.Is(probe.err, context.Canceled) {
			s.probeMu.Lock()
			if s.probe == probe {
				// 仅移除自己创建的取消条目，不能清掉另一组配置的新探测。
				s.probe = nil
			}
			s.probeMu.Unlock()
		}
		// channel 关闭同时发布结果，等待方不读取尚未完成的 err 字段。
		close(probe.done)
		return probe.err
	}
	s.probeMu.Unlock()
	// 等待不能持锁；单项取消立即返回，不依赖另一个网络请求完成。
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-probe.done:
		// 其他项取消不是自身失败；重新竞争一次预检，其余工作者仍可共享。
		if errors.Is(probe.err, context.Canceled) && ctx.Err() == nil {
			return s.ensureVerificationAccess(ctx, key, run)
		}
		return probe.err
	}
}

// preflightVerificationNetwork 在与验收相同的权限配置下检查日志传输。
// 参数 ctx：本轮可取消上下文；参数 cli：已发现的 CLI 文件路径。
// 参数 config：固定的非敏感设置，主机清单来自用户配置。
// 返回：任何配置主机不通则阻止模型启动；不读取密钥、不修改全局配置。
func (s *Service) preflightVerificationNetwork(ctx context.Context, cli string, config workspace.Settings) error {
	// 配置快照来自入队时，运行中编辑设置不会悄悄改变当前权限。
	domains := workspace.LogDomains(config)
	// 参数检查失败不降级沙箱，也不尝试完全访问模式。
	permissions, err := VerificationPermissionArguments(config)
	if err != nil {
		return err
	}
	key := cli + "\n" + strings.Join(domains, "\n")
	// 缓存键含完整允许主机集，新增主机不能沿用另一组网络检查。
	return s.ensureVerificationAccess(ctx, key, func(parent context.Context) error {
		// 自身 EXE 提供无状态探测入口，不依赖 Python 别名或用户 shell 配置。
		self, err := os.Executable()
		if err != nil {
			return err
		}
		// 每个主机独立限时；CLI 及其子进程由现有 Job 管理，停止时一起结束。
		for _, domain := range domains {
			probeCtx, cancel := context.WithTimeout(parent, (policy.VerificationProbeSeconds+5)*time.Second)
			args := append([]string{"sandbox", "-P", "release_desk_logs_readonly"}, permissions...)
			// 参数数组保留主机和路径边界，配置不能注入额外命令。
			args = append(args, "-C", config.CodeRepository, "--", self, "--probe-log-network", domain)
			cmd := process.Command(probeCtx, cli, args...)
			// 丢弃原始 CLI 输出，防止用户配置诊断意外进入业务历史。
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			err = process.RunManagedCommand(cmd)
			// 取消保留其身份，其他等待者不会把取消复用成网络故障。
			if parent.Err() != nil {
				cancel()
				return parent.Err()
			}
			cancel()
			// 失败只说明取证工具不可用，不能生成“业务失败”或“没有日志”的假结论。
			if err != nil {
				return fmt.Errorf("取证受阻：日志网络预检未通过（%s）；未启动 AI，请检查 CLI 权限配置及网络后重试", domain)
			}
		}
		// 传输通过后模型仍需真实查询，预检不替代鉴权、样本和代码验收。
		return nil
	})
}
