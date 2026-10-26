package app

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	aiservice "release-desk/internal/ai"
	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/identity"
	"release-desk/internal/platform/desktop"
	"release-desk/internal/storage/workspace"
	"release-desk/web"
)

var assets = web.Assets

// Main 解析启动参数并进入桌面工作台，供 cmd/releasedesk 调用。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 不返回业务值；运行结果通过受控状态、记录或日志反馈，失败不会伪装为成功。
func Main() {
	// 默认数据目录属于当前用户，不随 EXE 覆盖。
	// 命令行参数可为测试指定隔离目录。
	// 只读检查模式与正常桌面启动分开。
	// 启动失败记录原因并给用户明确提示。
	// 桌面模式不弹出无关控制台。

	runtime.LockOSThread()
	root, _ := os.UserConfigDir()

	defaultData := filepath.Join(root, "ReleaseDesk")
	data := flag.String("data-dir", defaultData, "本地记录目录")

	serve := flag.Bool("serve", false, "仅启动本机服务，用于界面测试")
	check := flag.Bool("check", false, "检查数据与连接（只读，不执行 SQL）")
	probeDomain := flag.String("probe-log-network", "", "只检查日志主机网络，不启动桌面或读取业务文件")

	flag.Parse()
	// 预检子进程不争用主窗口锁、不恢复数据，也不写日志；退出码只表示网络可达性。
	if *probeDomain != "" {
		// 返回非零让外层沙箱预检阻止收费模型启动，不输出底层网络诊断或本机配置。
		if err := aiservice.ProbeLogNetwork(*probeDomain); err != nil {
			// 专用退出码由调用方解释，不弹出交互窗口阻塞后台预检。
			os.Exit(1)
		}
		return
	}
	// 项目工具链和安装包与用户数据分开，更新 EXE 不会覆盖发布记录。
	if err := run(*data, *serve, *check); err != nil {
		slog.Error("应用启动失败", "error", err)
		if !*serve && !*check {
			title, _ := windows.UTF16PtrFromString("Release Desk")
			text, _ := windows.UTF16PtrFromString(err.Error())
			windows.MessageBox(0, text, title, windows.MB_ICONERROR)
		}
		os.Exit(1)
	}
}

// run 编排存储、服务、窗口与退出收尾。
// 参数 data：当前用户数据根目录，可由 --data-dir 指定；测试时必须使用隔离目录。
// 参数 serve：是否仅运行本机服务；用于隔离的界面自动化测试。
// 参数 check：是否只执行连接和工具可用性检查；不会执行业务 SQL。
// 返回：正常关闭返回 nil；日志、存储、监听或 WebView 初始化失败时返回错误。
func run(data string, serve, check bool) error {
	// 先初始化日志再读取本地记录。
	// 记录文件与执行审计在启动时恢复。
	// 配置是启动服务的唯一环境来源。
	// 只读检查模式不启动窗口或执行任务。
	// HTTP 只监听本机回环地址。
	// 随机端口避免与用户其他服务冲突。
	// 每次启动生成新的能力令牌。
	// 服务关闭由启动函数负责收尾。
	// 文件监听只负责本地记录合并。
	// 界面资源来自 EXE 内嵌内容。
	// 原生窗口使用独立 WebView 数据目录。
	// 最小尺寸保证表格与操作区可用。
	// 最小化只隐藏窗口，后台任务照常继续。
	// 点击 × 取消已有任务并限时保存执行审计。
	// 未确认的 SQL 中断不能被当成成功或普通失败。
	// 界面不会把令牌放到外部页面链接。
	// 最终退出时记录生命周期结束。

	// 桌面窗口使用整机互斥，QA 本机服务仍可用隔离目录运行而不创建窗口。
	if !serve && !check {
		// 窗口锁先于文件恢复，重复启动不会误修改正在使用的执行审计。
		windowGuard, err := desktop.AcquireDesktopInstance()
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			// 正常结束第二个进程，不再弹出一个额外的启动错误窗口。
			if desktop.ActivateDesktopWindow() {
				return nil
			}
			// 上一实例刚关闭窗口时等待其释放锁，再继续这一次启动，避免双击无反应。
			windowGuard, err = desktop.AcquireDesktopInstance()
		}
		if err != nil {
			return fmt.Errorf("无法建立单窗口保护：%w", err)
		}
		defer windows.CloseHandle(windowGuard)
		// 句柄延迟到后台任务完成后释放，关闭主窗口时也不会出现执行并发。
	}
	if err := setupLog(data); err != nil {
		return err
	}
	// 同一份数据只允许一个进程持有，防止重复点击 EXE 同时执行同一脚本。
	guard, err := lockInstance(data)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(guard)
	store, err := workspace.OpenStore(data)

	if err != nil {
		return err
	}

	config := store.Config()
	app := &App{Store: store, Git: gitrepo.New(config.SQLRepository, config.GitAuthor), Token: identity.NewID()}

	if check {
		return app.check()
	}
	// 绑定随机本机端口；能力令牌仅注入本地页面，网页无法跨域调用执行接口。

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	app.Host = listener.Addr().String()
	server := &http.Server{Handler: app.routes(), ReadHeaderTimeout: 5 * time.Second}

	go server.Serve(listener)
	// 桌面启动即在独立协程检查软件更新，隔离 QA 服务默认不访问公网。
	if !serve {
		go app.services().Updates.Run()
	}

	// × 退出不能被未完成的 HTTP 预览请求拖住，业务写入由 Store 收尾保存审计。
	defer server.Close()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			store.ScanFiles()
		}
	}()

	url := "http://" + app.Host

	slog.Info("应用启动", "address", url, "data_dir", data)
	if serve {
		fmt.Println(url)
		select {}
	}

	window := webview.NewWithOptions(webview.WebViewOptions{Debug: false, AutoFocus: true, DataPath: filepath.Join(data, "WebView"), WindowOptions: webview.WindowOptions{Title: desktop.DesktopWindowTitle, Width: 1440, Height: 940, Center: true}})
	if window == nil {
		return errors.New("无法加载 WebView2，请安装 Microsoft Edge WebView2 Runtime")
	}

	defer window.Destroy()
	// － 收进托盘继续工作，× 取消任务并结束进程；两种操作不能共用隐藏逻辑。
	tray, err := desktop.InstallDesktopTray(uintptr(window.Window()), store.RequestClose)
	if err != nil {
		return err
	}
	defer tray.Close()

	window.SetSize(1100, 740, webview.HintMin)

	window.Navigate(url)
	// 主窗口与托盘都完成初始化后记录就绪，便于区分后台服务和可恢复的桌面窗口。
	slog.Info("主窗口已就绪")

	window.Run()
	slog.Info("主窗口已关闭，开始退出收尾")
	// 真正关闭时主动取消并限时收尾，不能遗留没有窗口却占用单实例锁的进程。
	store.StopWindowTasks()

	slog.Info("应用退出")
	return nil
}

// lockInstance 为同一数据目录获取单进程持有的 Windows 互斥对象。
// 参数 data：当前数据根目录；不同测试目录使用独立实例，不与正常工作台争用。
// 返回：需要调用方关闭的系统句柄；相同目录已有进程时返回错误，不读取或修改其执行历史。
func lockInstance(data string) (windows.Handle, error) {
	// 同一个用户对相同数据目录重复启动时，不能创建第二个执行入口。
	// 互斥句柄由启动函数持有直到后台任务和窗口都完成收尾。
	// 互斥标识只使用规范路径摘要，不把个人路径或凭据暴露在系统对象名称中。
	// 必须在 openStore 前建立保护，不能把另一个运行进程的审计误判为异常退出。
	absolute, err := filepath.Abs(data)
	if err != nil {
		return 0, err
	}
	name, err := windows.UTF16PtrFromString(`Local\ReleaseDesk-` + identity.Digest(strings.ToLower(absolute)))
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		return 0, errors.New("此数据目录的 Release Desk 已在运行，请使用已有窗口")
	}
	// 系统在进程异常退出时释放句柄，下一次启动才能进入恢复流程。
	// 创建失败与已存在分别反馈，不能继续运行在没有保护的状态。
	return handle, err
}

// setupLog 初始化不含敏感正文的本机结构化日志。
// 参数 dir：当前用户的数据根目录；目录中只管理本应用记录和配置。
// 返回：成功返回 nil；日志目录或文件不可写时返回错误，启动不能继续假定日志已就绪。
func setupLog(dir string) error {
	// 日志按日期分文件，便于排查本机问题。
	// 目录使用当前用户的数据路径。
	// 文件权限与记录文件保持一致。

	folder := filepath.Join(dir, "logs")

	if e := os.MkdirAll(folder, 0700); e != nil {
		return e
	}
	// 日志只记录业务定位信息和汇总，不输出密码、SQL 正文或生产敏感日志。
	file, e := os.OpenFile(filepath.Join(folder, time.Now().Format("2006-01-02")+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)

	if e != nil {
		return e
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(file, nil)))
	return nil
}

// routes 注册内嵌界面资源与本机 API 路由。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回只服务本机内嵌界面与受鉴权 API 的 HTTP Handler。
func (a *App) routes() http.Handler {
	// 界面资源全部从内嵌文件读取。
	// 只接受当前本机监听地址的 Host。
	// 路径不能通过父目录跳转读取其他文件。
	// 根路径映射到应用首页。
	// 资源类型明确设置，避免浏览器错误解释。
	// 能力令牌仅注入首页。
	// 禁用远程脚本和远程连接。
	// 配置文本不能成为可执行页面代码。
	// 禁止其他站点嵌入本工作台。
	// 资源每次读取本进程版本，避免缓存旧令牌。
	// 未知资源返回不存在。

	mux := http.NewServeMux()

	mux.HandleFunc("/api/", a.api)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != a.Host {
			http.Error(w, "Forbidden", 403)
			return
		}
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if strings.Contains(path, "..") {
			http.NotFound(w, r)
			return
		}
		b, e := assets.ReadFile(strings.TrimPrefix(path, "/"))
		if e != nil {
			http.NotFound(w, r)
			return
		}
		// 不允许远程脚本或远程连接，界面资源全部内嵌 EXE。
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case strings.HasSuffix(path, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			b = []byte(strings.ReplaceAll(string(b), "__TOKEN__", a.Token))
			// 首屏直接使用已保存主题，避免端口改变导致偏好丢失或亮色闪屏。
			a.Store.Mu.Lock()
			// 只复制受限主题标识，锁内不写网络响应或加载文件。
			theme := a.Store.Theme
			a.Store.Mu.Unlock()
			if !slices.Contains(workspace.Themes, theme) {
				// 未初始化的测试 Store 也使用低亮度默认值。
				theme = workspace.DefaultTheme
			}
			b = []byte(strings.ReplaceAll(string(b), "__THEME__", theme))
			// 替换值来自有限集合，不把任意配置文本插入 HTML 属性。
		case strings.HasSuffix(path, ".js"):
			w.Header().Set("Content-Type", "application/javascript")
		case strings.HasSuffix(path, ".css"):
			w.Header().Set("Content-Type", "text/css")
		}
		w.Write(b)
	})

	return mux
}

// api 统一处理本机鉴权、请求解析与响应。
// 参数 w：HTTP 响应对象，只返回经过统一编码的本机接口结果。
// 参数 r：本机 HTTP 请求，需先验证方法、Host 与能力令牌。
// 返回本次操作的结果；包含 error 的签名在校验、读取或持久化失败时返回明确错误，调用方不得继续当作成功。
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	// 仅接受 POST 访问本机接口。
	// Host 与启动时绑定的地址必须一致。
	// 令牌用常量时间方式比较。
	// 请求体限制防止本机错误输入耗尽内存。
	// 未知操作不交给任意反射或 shell。
	// 业务请求体可能包含密码或配置，不进入日志。
	// 统一返回结构方便界面明确区分错误。
	// 只记录操作名称和耗时。
	// 轮询状态不反复打印成功日志。

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	if r.Method != "POST" || r.Host != a.Host || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Release-Token")), []byte(a.Token)) != 1 {
		http.Error(w, `{"error":"请求未授权"}`, 403)
		return
	}

	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if e != nil {
		http.Error(w, `{"error":"请求过大"}`, 400)
		return
	}

	op := strings.TrimPrefix(r.URL.Path, "/api/")

	handler, ok := a.handlers()[op]
	if !ok {
		http.NotFound(w, r)
		return
	}

	started := time.Now()

	result, e := handler(body)
	if e != nil {
		// 参数可能包含密码或配置内容，因此不记录请求体和完整数据库错误。
		slog.Warn("操作失败", "operation", op, "duration_ms", time.Since(started).Milliseconds())
		json.NewEncoder(w).Encode(map[string]any{"error": e.Error()})
		return
	}

	if op != "state" {
		slog.Info("操作完成", "operation", op, "duration_ms", time.Since(started).Milliseconds())
	}
	json.NewEncoder(w).Encode(map[string]any{"data": result})
}
