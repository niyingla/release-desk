Release Desk
============

Windows 单机版本发版工作台，使用 Go + WebView2，数据保存在当前用户电脑。

功能
- 版本与归档管理；定时任务、配置、SQL、Topic、变更与验证五类清单。
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
AI 功能需要用户安装并登录 Codex CLI，并自行配置仓库与技能路径。

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
例如准备发布 1.9.1：先把版本记录改成 1.9.1，再执行：
  ./build.ps1 -ArchiveOnly
  git status
  git add .
  git commit -m "feat(release-1.9.1): 本次软件更新说明"
  git push origin main
  git tag v1.9.1
  git push origin v1.9.1
1.9.1 只是示例，标签必须与当前软件版本一致。到仓库 Actions 页面确认绿色成功，再到 Releases 查看附件。
构建失败不会发布新版本。内嵌更新来源位于 internal/update/source.json，Fork 后修改仓库标识。
依赖许可由其各自项目规定；内嵌第三方前端库许可保留在 web/scripts/vendor 下。
