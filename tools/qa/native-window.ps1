param([string]$Executable)
$ErrorActionPreference = 'Stop'
$taskRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
if (-not $Executable) { $Executable = Join-Path $taskRoot 'dist\ReleaseDesk.exe' }
$Executable = (Resolve-Path -LiteralPath $Executable).Path
$taskData = Join-Path $taskRoot ('.work\qa\data\native-tray-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
$taskDuplicateData = $taskData + '-duplicate'

# 所有进程与数据均属于本轮测试，不访问真实数据库或启动收费 Codex 任务。
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;
public static class ReleaseDeskWindowQA {
 public delegate bool EnumProc(IntPtr window,IntPtr parameter);
 [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc callback,IntPtr parameter);
 [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr window,out uint process);
 [DllImport("user32.dll",EntryPoint="GetPropW",CharSet=CharSet.Unicode)] public static extern IntPtr GetProp(IntPtr window,string name);
 [DllImport("user32.dll")] public static extern IntPtr SendMessageW(IntPtr window,uint message,UIntPtr wParam,IntPtr lParam);
 [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr window,int state);
 [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr window);
 [DllImport("user32.dll")] public static extern bool PostMessageW(IntPtr window,uint message,UIntPtr wParam,IntPtr lParam);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] public static extern int GetClassNameW(IntPtr window,StringBuilder name,int length);
 public struct Rect { public int Left,Top,Right,Bottom; }
 public struct Point { public int X,Y; }
 [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr window,out Rect rect);
 [DllImport("user32.dll")] public static extern bool SetCursorPos(int x,int y);
 [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(Point point);
 [DllImport("user32.dll")] public static extern void mouse_event(uint flags,uint x,uint y,uint data,UIntPtr extra);
 public static bool ChooseExit(IntPtr menu,uint wanted) {
  Rect rect;if(!GetWindowRect(menu,out rect)||rect.Right<=rect.Left||rect.Bottom<=rect.Top)return false;
  var point=new Point{X=(rect.Left+rect.Right)/2,Y=rect.Top+(rect.Bottom-rect.Top)*3/4};
  uint process;GetWindowThreadProcessId(WindowFromPoint(point),out process);
  if(process!=wanted) return false;
  SetCursorPos(point.X,point.Y);
  mouse_event(2,0,0,0,UIntPtr.Zero);mouse_event(4,0,0,0,UIntPtr.Zero);
  return true;
 }
 public static IntPtr FindMenu(uint wanted) {
  IntPtr result=IntPtr.Zero;
  EnumWindows((window,parameter)=>{
   uint process;GetWindowThreadProcessId(window,out process);
   var name=new StringBuilder(128);GetClassNameW(window,name,name.Capacity);
   if(process==wanted && name.ToString()=="#32768") {result=window;return false;}
   return true;
  },IntPtr.Zero);
  return result;
 }
 public static IntPtr Find(uint wanted) {
  IntPtr result=IntPtr.Zero;
  EnumWindows((window,parameter)=>{
   uint process;GetWindowThreadProcessId(window,out process);
   if(process==wanted && GetProp(window,"ReleaseDesk.MainWindow")!=IntPtr.Zero) {result=window;return false;}
   return true;
  },IntPtr.Zero);
  return result;
 }
}
'@

function Wait-TaskWindow([Diagnostics.Process]$Process) {
    # 专属标识和 PID 同时匹配，不依赖标题搜索，也不操作其他应用窗口。
    $taskDeadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $taskDeadline) {
        if ($Process.HasExited) { throw '测试进程在窗口就绪前退出' }
        $taskWindow = [ReleaseDeskWindowQA]::Find([uint32]$Process.Id)
        if ($taskWindow -ne [IntPtr]::Zero) { return $taskWindow }
        Start-Sleep -Milliseconds 100
    }
    throw '新版没有建立带专属标识的主窗口'
}

function Close-TaskWindow([IntPtr]$Window, [Diagnostics.Process]$Process) {
    # 发送与点击 × 相同的窗口消息，让软件自己的取消和审计收尾逻辑执行。
    [void][ReleaseDeskWindowQA]::SendMessageW($Window, 0x0010, [UIntPtr]::Zero, [IntPtr]::Zero)
    if (-not $Process.WaitForExit(6000)) { throw '点击 × 后仍有无窗口进程占用实例锁' }
    if ($Process.ExitCode -ne 0) { throw ('软件退出返回错误状态：' + $Process.ExitCode) }
}

$taskFirst = $null
$taskThird = $null
try {
    $taskFirst = Start-Process -FilePath $Executable -ArgumentList @('--data-dir', ('"' + $taskData + '"')) -WindowStyle Hidden -PassThru
    $taskWindow = Wait-TaskWindow $taskFirst
    # 测试消息到达 UI 线程前，先等就绪日志，保证已安装完整的托盘钩子。
    $taskLog = Join-Path $taskData ('logs\' + (Get-Date -Format 'yyyy-MM-dd') + '.log')
    $taskDeadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $taskDeadline) {
        if ((Test-Path -LiteralPath $taskLog) -and (Select-String -LiteralPath $taskLog -Pattern '主窗口已就绪' -Quiet)) { break }
        Start-Sleep -Milliseconds 100
    }
    [void][ReleaseDeskWindowQA]::ShowWindow($taskWindow, 5)
    [void][ReleaseDeskWindowQA]::SendMessageW($taskWindow, 0x0112, [UIntPtr]0xf020, [IntPtr]::Zero)
    if ([ReleaseDeskWindowQA]::IsWindowVisible($taskWindow)) { throw '点击 － 未收进系统托盘' }
    [void][ReleaseDeskWindowQA]::SendMessageW($taskWindow, 0x8011, [UIntPtr]::Zero, [IntPtr]0x0202)
    if (-not [ReleaseDeskWindowQA]::IsWindowVisible($taskWindow)) { throw '点击托盘未恢复原窗口' }

    # 再收进托盘，从不同数据目录重复启动，仍必须恢复原来的同一个窗口。
    [void][ReleaseDeskWindowQA]::SendMessageW($taskWindow, 0x0112, [UIntPtr]0xf020, [IntPtr]::Zero)
    $taskSecond = Start-Process -FilePath $Executable -ArgumentList @('--data-dir', ('"' + $taskDuplicateData + '"')) -WindowStyle Hidden -PassThru
    if (-not $taskSecond.WaitForExit(6000) -or $taskSecond.ExitCode -ne 0) { throw '重复启动进程没有正常退出' }
    $taskDeadline = (Get-Date).AddSeconds(2)
    while ((Get-Date) -lt $taskDeadline -and -not [ReleaseDeskWindowQA]::IsWindowVisible($taskWindow)) { Start-Sleep -Milliseconds 50 }
    if (-not [ReleaseDeskWindowQA]::IsWindowVisible($taskWindow)) { throw '重复启动未从托盘恢复原窗口' }
    if (Test-Path -LiteralPath $taskDuplicateData) { throw '重复启动创建了第二份数据和执行入口' }

    Close-TaskWindow $taskWindow $taskFirst
    $taskThird = Start-Process -FilePath $Executable -ArgumentList @('--data-dir', ('"' + $taskData + '"')) -WindowStyle Hidden -PassThru
    $taskReopened = Wait-TaskWindow $taskThird
    # 窗口属性在导航前建立；等待界面初始化请求，避免把初始化中的 COM 重入当成用户正常关闭。
    $taskDeadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $taskDeadline) {
        if (@(Select-String -LiteralPath $taskLog -Pattern '主窗口已就绪').Count -ge 2 -and @(Select-String -LiteralPath $taskLog -Pattern 'git/versions').Count -ge 1) { break }
        Start-Sleep -Milliseconds 100
    }
    # 从隐藏状态打开真实右键菜单，定向向该应用投递菜单键，不操作其他软件。
    [void][ReleaseDeskWindowQA]::SendMessageW($taskReopened, 0x0112, [UIntPtr]0xf020, [IntPtr]::Zero)
    [void][ReleaseDeskWindowQA]::PostMessageW($taskReopened, 0x8011, [UIntPtr]::Zero, [IntPtr]0x0205)
    $taskDeadline = (Get-Date).AddSeconds(5)
    $taskMenu = [IntPtr]::Zero
    while ((Get-Date) -lt $taskDeadline -and $taskMenu -eq [IntPtr]::Zero) {
        $taskMenu = [ReleaseDeskWindowQA]::FindMenu([uint32]$taskThird.Id)
        Start-Sleep -Milliseconds 50
    }
    if ($taskMenu -eq [IntPtr]::Zero) { throw '托盘右键没有显示原生菜单' }
    # 仅点击已核实属于本轮进程的两项菜单的第二项，不通过杀进程代替退出。
    if (-not [ReleaseDeskWindowQA]::ChooseExit($taskMenu,[uint32]$taskThird.Id)) { throw '菜单位置不属于测试进程，拒绝点击其他应用' }
    if (-not $taskThird.WaitForExit(6000) -or $taskThird.ExitCode -ne 0) { throw '右键菜单退出后进程未正常结束' }
    if (-not (Select-String -LiteralPath $taskLog -Pattern '用户从托盘请求退出' -Quiet)) { throw '没有触发真实托盘退出命令' }
    Write-Output 'NATIVE PASS: 最小化、托盘恢复、重复启动恢复、× 退出、重启、真实右键菜单退出；无真实 SQL/Codex 调用'
} finally {
    # 失败时也只对本轮 PID 的已标记窗口发送关闭，不终止用户的其他实例或任务。
    foreach ($taskProcess in @($taskFirst, $taskThird)) {
        if ($taskProcess -and -not $taskProcess.HasExited) {
            $taskWindow = [ReleaseDeskWindowQA]::Find([uint32]$taskProcess.Id)
            if ($taskWindow -ne [IntPtr]::Zero) { [void][ReleaseDeskWindowQA]::SendMessageW($taskWindow, 0x0010, [UIntPtr]::Zero, [IntPtr]::Zero) }
        }
    }
}
