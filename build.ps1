param([switch]$SkipTests, [switch]$ArchiveOnly, [switch]$SkipDesktopCopy)
$ErrorActionPreference = 'Stop'
$taskRoot = $PSScriptRoot
$taskGo = Join-Path $taskRoot '.tools\go\bin\go.exe'
if (-not (Test-Path -LiteralPath $taskGo)) { $taskGo = (Get-Command go -ErrorAction Stop).Source }
Push-Location $taskRoot
try {
    # 更新记录是软件版本的唯一来源；业务 5.x 发布记录不参与程序版本编号。
    $taskCatalogPath = Join-Path $taskRoot 'internal\versioninfo\releases.json'
    $taskCatalog = Get-Content -LiteralPath $taskCatalogPath -Raw | ConvertFrom-Json
    $taskVersion = $taskCatalog.current
    if ($taskVersion -notmatch '^\d+\.\d+\.\d+$' -or $taskCatalog.releases[0].version -ne $taskVersion) { throw '软件版本目录无效' }
    $taskBuildID = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssfffZ')
    $taskBuiltAt = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    $taskStage = Join-Path $taskRoot ('.work\builds\' + $taskBuildID)
    New-Item -ItemType Directory -Path $taskStage | Out-Null
    # 同步 Windows manifest 后再生成链接资源，避免内嵌标识与界面版本不一致。
    $taskManifestPath = Join-Path $taskRoot 'build\windows\app.manifest'
    $taskManifest = Get-Content -LiteralPath $taskManifestPath -Raw
    $taskManifest = $taskManifest -replace 'assemblyIdentity version="[^"]+"', ('assemblyIdentity version="' + $taskVersion + '.0"')
    [IO.File]::WriteAllText($taskManifestPath, $taskManifest, [Text.UTF8Encoding]::new($false))
    $taskRsrc = Get-Command rsrc -ErrorAction SilentlyContinue
    $taskRsrcPath = if ($taskRsrc) { $taskRsrc.Source } else { Join-Path $env:USERPROFILE 'go\bin\rsrc.exe' }
    if (-not (Test-Path -LiteralPath $taskRsrcPath)) { throw '缺少 rsrc 资源构建工具，请安装后重试' }
    & $taskRsrcPath -arch amd64 -manifest $taskManifestPath -ico (Join-Path $taskRoot 'build\windows\app.ico') -o (Join-Path $taskRoot 'cmd\releasedesk\resources_windows.syso')
    if ($LASTEXITCODE -ne 0) { throw 'Windows 资源构建失败' }
    # 只归档列明的源码与工程文件；本机数据、凭据、依赖缓存不进入快照。
    $taskSourcePaths = @('cmd','internal','web','build','tools','examples','go.mod','go.sum','build.ps1','package.json','.gitignore','使用说明.txt')
    if (Test-Path -LiteralPath (Join-Path $taskRoot 'package-lock.json')) { $taskSourcePaths += 'package-lock.json' }
    # 发布工作流与工程说明属于可重建源码；CI 不依赖本机工具链和私有运行数据。
    foreach ($taskOptional in @('.github','readme.txt','.prettierignore','AGENTS.md')) {
        if (Test-Path -LiteralPath (Join-Path $taskRoot $taskOptional)) { $taskSourcePaths += $taskOptional }
    }
    # 示例采用公开白名单；本机业务示例及历史生成脚本虽然被 Git 忽略，也不能混入源码 ZIP。
    $taskPublicExamples = @('template.json')
    $taskSourceFilter = {
        $taskRelative = [IO.Path]::GetRelativePath($taskRoot, $_.FullName).Replace('\','/')
        -not $taskRelative.StartsWith('tools/examples/') -and (-not $taskRelative.StartsWith('examples/') -or $taskPublicExamples -contains [IO.Path]::GetFileName($taskRelative))
    }
    $taskSourceFiles = @($taskSourcePaths | ForEach-Object { Get-ChildItem -LiteralPath (Join-Path $taskRoot $_) -Recurse -File } | Where-Object $taskSourceFilter | Sort-Object FullName)
    $taskSourceHashes = @($taskSourceFiles | ForEach-Object { [ordered]@{path=[IO.Path]::GetRelativePath($taskRoot,$_.FullName).Replace('\','/');sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()} })
    $taskSourceZip = Join-Path $taskStage 'source.zip'
    Add-Type -AssemblyName System.IO.Compression
    $taskZip = [IO.Compression.ZipFile]::Open($taskSourceZip, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($taskFile in $taskSourceFiles) {
            [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($taskZip, $taskFile.FullName, [IO.Path]::GetRelativePath($taskRoot,$taskFile.FullName).Replace('\','/')) | Out-Null
        }
    } finally { $taskZip.Dispose() }
    $taskSourceSHA = (Get-FileHash -LiteralPath $taskSourceZip -Algorithm SHA256).Hash.ToLowerInvariant()
    # 测试只使用隔离目录和模拟数据库，构建脚本不执行真实业务 SQL。
    if (-not $SkipTests) {
        & $taskGo test -p 4 ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go 测试失败' }
        & $taskGo vet -p 4 ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go 静态检查失败' }
        & $taskGo run ./tools/commentcheck
        if ($LASTEXITCODE -ne 0) { throw '注释检查失败' }
    }
    # Windows GUI 子系统构建，无需 Go 或 Python 运行时；界面资源全部内嵌。
    $taskLinkFlags = '-H windowsgui -s -w -X release-desk/internal/versioninfo.BuildID=' + $taskBuildID + ' -X release-desk/internal/versioninfo.BuiltAt=' + $taskBuiltAt + ' -X release-desk/internal/versioninfo.SourceSHA256=' + $taskSourceSHA
    & $taskGo build -p 4 -trimpath -ldflags $taskLinkFlags -o (Join-Path $taskStage 'ReleaseDesk.exe') ./cmd/releasedesk
    if ($LASTEXITCODE -ne 0) { throw 'EXE 打包失败' }
    # 其他会话在构建期间修改源文件时拒绝发布，不能把不匹配的快照标成同一构建。
    $taskAfterFiles = @($taskSourcePaths | ForEach-Object { Get-ChildItem -LiteralPath (Join-Path $taskRoot $_) -Recurse -File } | Where-Object $taskSourceFilter | Sort-Object FullName)
    $taskAfterHashes = @($taskAfterFiles | ForEach-Object { [ordered]@{path=[IO.Path]::GetRelativePath($taskRoot,$_.FullName).Replace('\','/');sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()} })
    if ((ConvertTo-Json -InputObject $taskSourceHashes -Compress) -ne (ConvertTo-Json -InputObject $taskAfterHashes -Compress)) { throw '构建期间源码发生变化，请等其他会话完成后重试' }
    $taskExamples = Join-Path $taskStage 'examples'
    New-Item -ItemType Directory -Path $taskExamples -Force | Out-Null
    # 安装包与源码使用同一公开模板清单，不能通过第二条打包路径带出业务版本文件。
    foreach ($taskExample in $taskPublicExamples) { Copy-Item -LiteralPath (Join-Path $taskRoot ('examples/' + $taskExample)) -Destination $taskExamples -Force }
    Copy-Item -LiteralPath (Join-Path $taskRoot '使用说明.txt') -Destination $taskStage
    Copy-Item -LiteralPath $taskCatalogPath -Destination (Join-Path $taskStage '软件版本记录.json')
    $taskBuildRecord = [ordered]@{software_version=$taskVersion;build_id=$taskBuildID;built_at=$taskBuiltAt;source_sha256=$taskSourceSHA;source_files=$taskSourceHashes;checks=if($SkipTests){'skipped'}else{'go test -p 4 ./...; go vet -p 4 ./...; commentcheck passed'};git_revision=$null}
    $taskBuildRecord | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $taskStage '构建记录.json') -Encoding utf8
    # 安装包只收录运行文件、说明和示例，不包含测试数据、源码备份与本机凭据。
    $taskArchive = Join-Path $taskStage ('ReleaseDesk-' + $taskVersion + '-Windows-x64.zip')
    Compress-Archive -LiteralPath @((Join-Path $taskStage 'ReleaseDesk.exe'), (Join-Path $taskStage '使用说明.txt'), (Join-Path $taskStage '软件版本记录.json'), (Join-Path $taskStage '构建记录.json'), $taskExamples) -DestinationPath $taskArchive
    # 对已经生成的实际压缩包再次检查，避免只核对脚本规则却漏掉未来新增入口。
    & $taskGo run ./tools/packagecheck $taskSourceZip $taskArchive
    if ($LASTEXITCODE -ne 0) { throw '公开发布包内容检查失败' }
    # 校验表覆盖 EXE、安装包、源码包和构建记录，保留独立构建目录不覆盖旧包。
    @('ReleaseDesk.exe', [IO.Path]::GetFileName($taskArchive), 'source.zip', '构建记录.json', '软件版本记录.json') | ForEach-Object { $taskHash = (Get-FileHash -LiteralPath (Join-Path $taskStage $_) -Algorithm SHA256).Hash.ToLowerInvariant(); $taskHash + '  ' + $_ } | Set-Content -LiteralPath (Join-Path $taskStage 'SHA256SUMS.txt') -Encoding utf8
    $taskDist = Join-Path $taskRoot 'dist'
    $taskReleaseDir = Join-Path $taskDist ('releases\' + $taskVersion + '\' + $taskBuildID)
    New-Item -ItemType Directory -Path $taskReleaseDir | Out-Null
    # 显式枚举，保证发布快照完整复制且不覆盖旧构建目录。
    Get-ChildItem -LiteralPath $taskStage | Copy-Item -Destination $taskReleaseDir -Recurse
    # 旧程序正在使用时可独立出包，避免为替换 EXE 强行结束用户任务。
    if ($ArchiveOnly) {
        $taskVersionedExe = Join-Path $taskDist ('ReleaseDesk-' + $taskVersion + '.exe')
        Copy-Item -LiteralPath (Join-Path $taskStage 'ReleaseDesk.exe') -Destination $taskVersionedExe -Force
        Copy-Item -LiteralPath $taskArchive -Destination (Join-Path $taskDist ([IO.Path]::GetFileName($taskArchive))) -Force
        Write-Output ('已生成：' + $taskVersionedExe)
    } else {
        Copy-Item -LiteralPath (Join-Path $taskStage 'ReleaseDesk.exe') -Destination $taskDist -Force
        Copy-Item -LiteralPath $taskArchive -Destination (Join-Path $taskDist 'ReleaseDesk-Windows-x64.zip') -Force
        foreach ($taskName in @('使用说明.txt','软件版本记录.json','构建记录.json','examples')) {
            Copy-Item -LiteralPath (Join-Path $taskStage $taskName) -Destination $taskDist -Recurse -Force
        }
        Write-Output ('已生成：' + (Join-Path $taskDist 'ReleaseDesk.exe'))
    }
    Write-Output ('已归档：' + $taskReleaseDir)
    # 无交互桌面的 CI 显式跳过复制；普通本机构建仍每次自动同步桌面 dist。
    if ($SkipDesktopCopy) { return }
    # 两种出包方式统一同步到当前用户的桌面，兼容桌面重定向和不同 Windows 用户。
    $taskDesktop = [Environment]::GetFolderPath('Desktop')
    if ([string]::IsNullOrWhiteSpace($taskDesktop)) { throw ('无法定位桌面，构建已保留在：' + $taskReleaseDir) }
    $taskDesktopDist = Join-Path $taskDesktop 'dist'
    $taskDesktopExe = Join-Path $taskDesktopDist ('ReleaseDesk-' + $taskVersion + '.exe')
    try {
        New-Item -ItemType Directory -Path $taskDesktopDist -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $taskStage 'ReleaseDesk.exe') -Destination $taskDesktopExe -Force
        # 只有文件内容一致才报告同步完成，避免交付残缺或错误的 EXE。
        $taskExpectedHash = (Get-FileHash -LiteralPath (Join-Path $taskStage 'ReleaseDesk.exe') -Algorithm SHA256).Hash
        if ((Get-FileHash -LiteralPath $taskDesktopExe -Algorithm SHA256).Hash -ne $taskExpectedHash) { throw '桌面 EXE 校验失败' }
    } catch {
        throw ('桌面 EXE 同步失败；构建已保留在 ' + $taskReleaseDir + '。原因：' + $_.Exception.Message)
    }
    Write-Output ('已同步桌面：' + $taskDesktopExe)
} finally { Pop-Location }
