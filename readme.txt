Release Desk
============

Windows 单机版本发版工作台，使用 Go + WebView2，数据保存在当前用户电脑。

功能
- 版本与归档管理；定时任务、配置、SQL、Topic、接口初始化、变更与验证六类清单。
- Codex 只读扫描与自主验证；批量验证支持 1～6 并发。
- 测试/UAT SQL 范围预览、逐库执行与独立审计；需要用户自行配置连接。
- 系统托盘、单实例、主题切换和 GitHub 正式版更新检测。

下载
https://github.com/niyingla/release-desk/releases/latest
运行附件中的 ReleaseDesk-x.y.z.exe。Windows 需有 Microsoft Edge WebView2 Runtime。
软件启动后自动检查更新，每 6 小时复查，也可点击“检查更新”。
更新提示打开 GitHub 下载页；关闭旧版后再运行新版，不覆盖本机记录。

数据与连接
数据目录：%APPDATA%\ReleaseDesk。
数据库连接由当前 Windows 用户 DPAPI 加密保存在该目录，不包含在源码或发布包中。
examples/template.json 为通用示例；仓库不包含真实发版记录和业务 SQL。
AI 功能需要用户安装并登录 Codex CLI，再填写本机仓库与作者。
“连接与设置”按数据库、日志、代码与 AI、接口调用分类，切换分类保留未保存输入。
代码与 AI 页提供“AI 一键接入”：复制指令交给助手，核对非敏感设置并增量填写发布记录。
扫描、日志查询和环境路由规则内嵌在 EXE 中，随软件版本一起更新。
启动时刷新数据目录 AI模板/skills 中的软件管理副本及版本摘要，不覆盖个人 Codex 技能。
日志 MCP 连接或授权只读脚本仍由每台电脑单独配置，软件不附带密钥或登录态。
日志页的补充技能路径可选；没有配置数据源时会说明取证缺口，不伪造查询结果。
新建版本只列出代码库 release- 开头的本地及远端跟踪分支，去重后倒序，支持搜索和手动输入。
扫描启动前固定所选版本的提交 SHA，通过 Git 对象读取；不切分支、不 fetch/pull、不覆盖未提交内容。

本地构建
安装 Go（版本见 go.mod）和资源编译工具：
  go install github.com/akavel/rsrc@v0.10.2
PowerShell 执行：
  ./build.ps1 -ArchiveOnly
默认运行 Go 测试、静态检查和注释检查，以 4 路并行构建。
成功后归档到 dist/releases，并将带版本号的 EXE 复制到桌面 dist。
前端可选检查：npm ci，然后 npm run check。

发布
先更新 internal/versioninfo/releases.json 的软件版本和记录，提交经过验证的源码。
推送与软件版本一致的 vX.Y.Z 标签后，GitHub Actions 自动构建并发布 EXE、ZIP 和 SHA256 校验表。
更新版本目录后，核对实际版本并执行：
  $releaseVersion = (Get-Content internal/versioninfo/releases.json -Raw | ConvertFrom-Json).current
  ./build.ps1 -ArchiveOnly
  git status
  # 仅暂存本次核对过的源码和长期说明，不能加入本机凭据、真实发布数据或 .work。
  git add <本次核对后的文件路径>
  git commit -m "feat(release-${releaseVersion}): 本次软件更新说明"
  git push origin main
  git tag "v$releaseVersion"
  git push origin "v$releaseVersion"
普通 push main 只更新源码，不更新线上安装包。已发布标签不可移动；修改后使用新的软件版本。
到 Actions 确认构建成功，并检查最新正式 Release 的 EXE、ZIP、SHA256SUMS.txt 已上传完成。
最后下载公开 EXE 核对 SHA-256，再通过旧版检查更新确认可发现新版本。
构建失败不会发布新版本。内嵌更新来源位于 internal/update/source.json，Fork 后修改仓库标识。
依赖许可由其各自项目规定；内嵌第三方前端库许可保留在 web/scripts/vendor 下。
