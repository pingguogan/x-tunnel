package gui

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/x-tunnel/internal/client"
	"github.com/x-tunnel/internal/config"
	"github.com/x-tunnel/internal/protocol"
)

//go:embed frontend/*
var frontendFS embed.FS

// ServerProfile 服务端配置
type ServerProfile struct {
	Name          string   `json:"name"`
	Server        string   `json:"server"`
	Token         string   `json:"token"`
	Listen        []string `json:"listen"`
	Connections   int      `json:"connections"`
	Insecure      bool     `json:"insecure"`
	Fallback      bool     `json:"fallback"`
	ECHDomain     string   `json:"ech_domain"`
	DNSServer     string   `json:"dns_server"`
	ECHConfig     string   `json:"ech_config"`
	ECHConfigURL  string   `json:"ech_config_url"`
	IPStrategy    string   `json:"ip_strategy"`
	TargetIPs     []string `json:"target_ips"`
	UDPBlockPorts string   `json:"udp_block_ports"`
	TUNEnabled    bool     `json:"tun_enabled"`
	TUNName       string   `json:"tun_name"`
	TUNSubnet     string   `json:"tun_subnet"`
	TUNDNS        string   `json:"tun_dns"`
	TUNMode       string   `json:"tun_mode"`
}

// AppConfig 应用配置文件
type AppConfig struct {
	Profiles      []ServerProfile `json:"profiles"`
	ActiveProfile string          `json:"active_profile"`
}

// App GUI 应用
type App struct {
	appCfg    AppConfig
	profile   *ServerProfile
	pool      *client.ECHPool
	proxy     *client.Proxy
	mu        sync.RWMutex
	status    string
	logs      []LogEntry
	logMu     sync.RWMutex
	cfgPath   string
	port      int
	stopCh    chan struct{} // 停止信号
	manualStop bool        // 是否手动停止
}

// LogEntry 日志条目
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// NewApp 创建 GUI 应用
func NewApp() *App {
	cfgPath := getConfigPath()
	app := &App{
		status:  "未连接",
		cfgPath: cfgPath,
		stopCh:  make(chan struct{}),
	}
	app.loadConfig()
	if len(app.appCfg.Profiles) == 0 {
		app.appCfg.Profiles = []ServerProfile{{
			Name:          "默认",
			Listen:        []string{"socks5://0.0.0.0:1080"},
			Connections:   3,
			ECHDomain:     "cloudflare-ech.com",
			DNSServer:     "https://doh.pub/dns-query",
			UDPBlockPorts: "443",
		}}
	}
	if app.appCfg.ActiveProfile == "" && len(app.appCfg.Profiles) > 0 {
		app.appCfg.ActiveProfile = app.appCfg.Profiles[0].Name
	}
	app.profile = app.getProfile(app.appCfg.ActiveProfile)
	if app.profile == nil && len(app.appCfg.Profiles) > 0 {
		app.profile = &app.appCfg.Profiles[0]
	}
	return app
}

func getConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "x-tunnel-config.json"
	}
	return filepath.Join(filepath.Dir(exe), "x-tunnel-config.json")
}

func (a *App) loadConfig() {
	data, err := os.ReadFile(a.cfgPath)
	if err != nil {
		return
	}
	json.Unmarshal(data, &a.appCfg)
}

func (a *App) saveConfig() {
	data, err := json.MarshalIndent(a.appCfg, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(a.cfgPath, data, 0644)
}

func (a *App) getProfile(name string) *ServerProfile {
	for i := range a.appCfg.Profiles {
		if a.appCfg.Profiles[i].Name == name {
			return &a.appCfg.Profiles[i]
		}
	}
	return nil
}

// Run 运行 GUI 应用
func (a *App) Run() error {
	// 初始化日志系统
	a.InitLogger()

	// 获取空闲端口
	port, err := GetFreePort()
	if err != nil {
		return fmt.Errorf("获取端口失败: %v", err)
	}
	a.port = port

	// 启动 HTTP 服务
	go a.startHTTPServer(port)

	// Windows 使用系统托盘，其他平台直接打开浏览器
	if runtime.GOOS == "windows" {
		return a.runWithSystray()
	}

	// Linux/Mac 直接打开浏览器
	openBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
	log.Printf("[GUI] 控制面板: http://127.0.0.1:%d", port)
	log.Printf("[GUI] 按 Ctrl+C 退出")

	// 保持运行
	select {}
}

func (a *App) startHTTPServer(port int) {
	mux := http.NewServeMux()

	subFS, _ := fs.Sub(frontendFS, "frontend")
	mux.Handle("/", http.FileServer(http.FS(subFS)))

	mux.HandleFunc("/api/profiles", a.handleProfiles)
	mux.HandleFunc("/api/profile", a.handleProfile)
	mux.HandleFunc("/api/active", a.handleActive)
	mux.HandleFunc("/api/connect", a.handleConnect)
	mux.HandleFunc("/api/disconnect", a.handleDisconnect)
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/logs", a.handleLogs)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("[GUI] 控制面板启动: http://%s", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("[GUI] HTTP 服务失败: %v", err)
	}
}

// HTTP API 处理方法

func (a *App) handleProfiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a.appCfg)
}

func (a *App) handleProfile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodPost:
		var p ServerProfile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if p.Name == "" {
			p.Name = fmt.Sprintf("服务器 %d", len(a.appCfg.Profiles)+1)
		}
		a.appCfg.Profiles = append(a.appCfg.Profiles, p)
		a.saveConfig()
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodPut:
		var p ServerProfile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for i := range a.appCfg.Profiles {
			if a.appCfg.Profiles[i].Name == p.Name {
				a.appCfg.Profiles[i] = p
				break
			}
		}
		if a.profile != nil && a.profile.Name == p.Name {
			a.profile = a.getProfile(p.Name)
		}
		a.saveConfig()
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		for i := range a.appCfg.Profiles {
			if a.appCfg.Profiles[i].Name == name {
				a.appCfg.Profiles = append(a.appCfg.Profiles[:i], a.appCfg.Profiles[i+1:]...)
				break
			}
		}
		if a.appCfg.ActiveProfile == name && len(a.appCfg.Profiles) > 0 {
			a.appCfg.ActiveProfile = a.appCfg.Profiles[0].Name
			a.profile = &a.appCfg.Profiles[0]
		}
		a.saveConfig()
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func (a *App) handleActive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	a.appCfg.ActiveProfile = req.Name
	a.profile = a.getProfile(req.Name)
	a.saveConfig()
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	a.mu.Lock()
	if a.status == "已连接" || a.status == "连接中..." {
		a.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"status": a.status})
		return
	}
	a.status = "连接中..."
	a.manualStop = false
	a.stopCh = make(chan struct{})
	a.mu.Unlock()

	go a.connectWithRetry()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "connecting"})
}

func (a *App) connectWithRetry() {
	maxRetries := 10
	retryDelay := 3 * time.Second

	for i := 0; i < maxRetries; i++ {
		// 检查是否手动停止
		select {
		case <-a.stopCh:
			a.addLog("INFO", "连接已停止")
			return
		default:
		}

		a.mu.RLock()
		manualStop := a.manualStop
		a.mu.RUnlock()
		if manualStop {
			return
		}

		err := a.connect()
		if err == nil {
			// 连接成功，等待断开
			a.waitForDisconnect()
			return
		}

		a.addLog("ERROR", fmt.Sprintf("连接失败: %v", err))

		if i < maxRetries-1 {
			a.addLog("INFO", fmt.Sprintf("%v 后重试... (%d/%d)", retryDelay, i+1, maxRetries))

			// 等待重试延迟或停止信号
			select {
			case <-a.stopCh:
				a.addLog("INFO", "重连已停止")
				return
			case <-time.After(retryDelay):
				a.addLog("INFO", "正在重新连接...")
			}
		}
	}

	a.addLog("ERROR", "连接失败，已达到最大重试次数")
	a.mu.Lock()
	a.status = "未连接"
	a.mu.Unlock()
}

func (a *App) connect() error {
	p := a.profile
	if p == nil {
		return fmt.Errorf("没有选择配置")
	}

	a.addLog("INFO", fmt.Sprintf("正在连接 %s (fallback=%v) ...", p.Server, p.Fallback))

	// 校验 ECH 配置，失效时从 URL 重新获取并保存
	a.validateAndRefreshECHConfig(p)

	// 显示配置信息
	if p.ECHConfig != "" {
		a.addLog("INFO", fmt.Sprintf("使用缓存的 ECH 配置 (%d 字符)", len(p.ECHConfig)))
	} else if p.ECHConfigURL != "" {
		a.addLog("INFO", fmt.Sprintf("从 URL 获取 ECH 配置: %s", p.ECHConfigURL))
	} else if p.ECHDomain != "" {
		a.addLog("INFO", fmt.Sprintf("ECH 域名: %s, DNS: %s", p.ECHDomain, p.DNSServer))
	}
	if len(p.TargetIPs) > 0 {
		a.addLog("INFO", fmt.Sprintf("指定连接 IP: %v", p.TargetIPs))
	}
	a.addLog("INFO", fmt.Sprintf("连接数: %d", p.Connections))

	strategy := protocol.ParseIPStrategy(p.IPStrategy)

	blockPorts := make(map[int]struct{})
	if p.UDPBlockPorts != "" {
		for _, s := range splitStr(p.UDPBlockPorts, ",") {
			var port int
			fmt.Sscanf(s, "%d", &port)
			if port > 0 && port < 65536 {
				blockPorts[port] = struct{}{}
			}
		}
	}

	a.pool = client.NewECHPool(
		p.Server,
		p.Connections,
		p.TargetIPs,
		"",
		p.Token,
		p.Insecure,
		p.Fallback,
		p.ECHDomain,
		p.DNSServer,
		p.ECHConfig,
		p.ECHConfigURL,
		64*1024,
		// ECH 刷新成功回调：同步更新到配置文件
		func(echBase64 string) {
			p.ECHConfig = echBase64
			a.saveConfig()
		},
	)
	if err := a.pool.Start(); err != nil {
		return err
	}

	a.proxy = client.NewProxy(a.pool, strategy, blockPorts)

	a.mu.Lock()
	a.status = "已连接"
	a.mu.Unlock()
	a.addLog("INFO", "连接成功")

	// 启动 TUN 模式
	if p.TUNEnabled {
		tunCfg := config.TUNConfig{
			Enabled:   true,
			Name:      p.TUNName,
			Subnet:    p.TUNSubnet,
			MTU:       1420,
			DNS:       strings.Split(p.TUNDNS, ","),
			Mode:      p.TUNMode,
			AutoRoute: true,
		}
		if tunCfg.Name == "" {
			tunCfg.Name = "xtun"
		}
		if tunCfg.Subnet == "" {
			tunCfg.Subnet = "10.0.0.1/24"
		}
		if len(tunCfg.DNS) == 0 || tunCfg.DNS[0] == "" {
			tunCfg.DNS = []string{"1.1.1.1", "8.8.8.8"}
		}
		if tunCfg.Mode == "" {
			tunCfg.Mode = "global"
		}

		tunDevice, err := client.NewTUNDevice(a.pool, tunCfg)
		if err != nil {
			a.addLog("ERROR", fmt.Sprintf("创建 TUN 设备失败: %v", err))
		} else {
			if err := tunDevice.Start(); err != nil {
				a.addLog("ERROR", fmt.Sprintf("启动 TUN 设备失败: %v", err))
			} else {
				a.addLog("INFO", fmt.Sprintf("TUN 模式已启用: %s (%s)", tunCfg.Name, tunCfg.Subnet))
			}
		}
	}

	for _, rule := range p.Listen {
		rule := rule
		if rule == "" {
			continue
		}
		go func() {
			if len(rule) > 6 && rule[:6] == "tcp://" {
				a.addLog("INFO", "TCP 转发: "+rule)
				a.proxy.RunTCPListener(rule)
			} else if len(rule) > 9 && rule[:9] == "socks5://" {
				a.addLog("INFO", "SOCKS5 代理: "+rule)
				a.proxy.RunSOCKS5Listener(rule)
			} else if len(rule) > 7 && rule[:7] == "http://" {
				a.addLog("INFO", "HTTP 代理: "+rule)
				a.proxy.RunHTTPListener(rule)
			}
		}()
	}

	return nil
}

// validateAndRefreshECHConfig 校验 ECH 配置，无效或为空时从 URL 获取并缓存
func (a *App) validateAndRefreshECHConfig(p *ServerProfile) {
	if p.Fallback {
		return
	}
	if p.ECHConfigURL == "" {
		return
	}

	// 有缓存且 base64 有效，直接使用
	if p.ECHConfig != "" {
		if _, err := base64.StdEncoding.DecodeString(p.ECHConfig); err == nil {
			return
		}
		a.addLog("WARN", "缓存的 ECH 配置无效，重新获取")
	}

	a.addLog("INFO", fmt.Sprintf("从 ECH 配置 URL 获取: %s", p.ECHConfigURL))

	echBase64, err := a.fetchECHConfigFromURL(p.ECHConfigURL)
	if err != nil {
		a.addLog("ERROR", fmt.Sprintf("从 URL 获取 ECH 配置失败: %v", err))
		return
	}

	if _, err := base64.StdEncoding.DecodeString(echBase64); err != nil {
		a.addLog("ERROR", fmt.Sprintf("从 URL 获取的 ECH 配置无效: %v", err))
		return
	}

	p.ECHConfig = echBase64
	a.saveConfig()
	a.addLog("INFO", fmt.Sprintf("ECH 配置已获取并缓存 (%d 字符)", len(echBase64)))
}

// fetchECHConfigFromURL 从 URL 获取 ECH 配置的 base64 字符串
func (a *App) fetchECHConfigFromURL(configURL string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(configURL)
	if err != nil {
		return "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}

	// 尝试 JSON 解析
	var jsonResp struct {
		ECH     string `json:"ech"`
		HasECH  *bool  `json:"has_ech"`
		Records []struct {
			Data string `json:"data"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &jsonResp); err == nil {
		// 格式 A: {"ech": "base64..."}
		if jsonResp.ECH != "" {
			return jsonResp.ECH, nil
		}
		// 格式 B: {"has_ech": true, "records": [{"data": "1 . ech=BASE64..."}]}
		if jsonResp.HasECH != nil && *jsonResp.HasECH {
			for _, r := range jsonResp.Records {
				if ech := extractECHFromHTTPSData(r.Data); ech != "" {
					return ech, nil
				}
			}
		}
		return "", fmt.Errorf("JSON 响应中未找到 ECH 配置")
	}

	// 尝试作为纯 base64 文本
	s := strings.TrimSpace(string(body))
	if _, err := base64.StdEncoding.DecodeString(s); err == nil {
		return s, nil
	}
	if _, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return s, nil
	}

	return "", fmt.Errorf("无法解析响应格式（非 JSON 也非 base64）")
}

// extractECHFromHTTPSData 从 HTTPS 记录的 data 字段提取 ECH 配置
func extractECHFromHTTPSData(data string) string {
	for _, part := range strings.Fields(data) {
		if strings.HasPrefix(part, "ech=") {
			return strings.TrimPrefix(part, "ech=")
		}
	}
	return ""
}

func (a *App) waitForDisconnect() {
	// 监控连接状态，直到断开
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			// 检查连接池是否还活着
			if a.pool != nil {
				// 连接池内部有重连逻辑，这里只监控状态
				continue
			}
			// 连接池为空，说明已断开
			a.addLog("INFO", "连接已断开，准备重连...")
			a.mu.Lock()
			a.status = "未连接"
			a.mu.Unlock()

			// 自动重连
			if !a.manualStop {
				go a.connectWithRetry()
			}
			return
		}
	}
}

func (a *App) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}

	a.mu.Lock()
	a.manualStop = true
	a.status = "未连接"

	// 停止连接池
	if a.pool != nil {
		a.pool.Stop()
		a.pool = nil
	}
	a.proxy = nil

	// 发送停止信号
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	a.mu.Unlock()

	a.addLog("INFO", "已断开连接")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	status := a.status
	a.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	a.logMu.RLock()
	logs := make([]LogEntry, len(a.logs))
	copy(logs, a.logs)
	a.logMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

func (a *App) addLog(level, message string) {
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	}
	a.logMu.Lock()
	a.logs = append(a.logs, entry)
	// 保留最近 2000 条日志
	if len(a.logs) > 2000 {
		a.logs = a.logs[len(a.logs)-2000:]
	}
	a.logMu.Unlock()
}

func splitStr(s, sep string) []string {
	var result []string
	for _, part := range split(s, sep) {
		part = trimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func split(s, sep string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			result = append(result, s[start:i])
			start = i + len(sep)
		}
	}
	result = append(result, s[start:])
	return result
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// GetFreePort 获取空闲端口
func GetFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
