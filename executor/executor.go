package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/newde36524/timer/database"
	"github.com/newde36524/timer/models"
	"github.com/newde36524/timer/scheduler"
)

// Executor HTTP 任务执行器
type Executor struct {
	client   *http.Client
	scheduler *scheduler.Scheduler
}

// NewExecutor 创建执行器
func NewExecutor(s *scheduler.Scheduler) *Executor {
	return &Executor{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		scheduler: s,
	}
}

// Execute 执行任务
func (e *Executor) Execute(task *models.TimerTask) {
	var lastErr error
	var statusCode int
	var responseBody string
	var execCommand string
	var success bool
	retryCount := 0

	// 根据执行模式选择执行方式
	for i := 0; i <= task.MaxRetryCount; i++ {
		retryCount = i
		
		if task.ExecMode == models.ExecModeScript {
			// 脚本执行模式
			statusCode, responseBody, execCommand, lastErr = e.executeScript(task)
		} else {
			// HTTP 请求模式（默认）
			statusCode, responseBody, execCommand, lastErr = e.doRequest(task)
		}
		
		if lastErr == nil && statusCode >= 200 && statusCode < 300 {
			success = true
			break
		}
		
		// 如果不是最后一次重试，等待一段时间
		if i < task.MaxRetryCount {
			time.Sleep(time.Second * time.Duration(i+1))
		}
	}

	// 记录执行日志
	log := &models.TaskExecuteLog{
		TaskKey:     task.Key,
		ExecTime:    time.Now().Unix(),
		Success:     success,
		StatusCode:  statusCode,
		RetryCount:  retryCount,
		ExecCommand: execCommand,
	}
	
	if lastErr != nil {
		log.Message = fmt.Sprintf("执行错误: %s", lastErr.Error())
	} else if !success {
		log.Message = fmt.Sprintf("失败(状态码:%d) - %s", statusCode, truncateString(responseBody, 5000))
	} else {
		log.Message = fmt.Sprintf("成功 - %s", truncateString(responseBody, 5000))
	}

	database.CreateExecuteLog(log)

	// 更新任务状态
	if success {
		task.ExecCount++
		task.LastExecTime = time.Now().Unix()
		
		// 检查是否达到最大执行次数
		if task.MaxExecCount > 0 && task.ExecCount >= task.MaxExecCount {
			task.Status = models.TaskStatusFinished
		} else {
			// 计算下次执行时间
			task.NextExecTime = task.CalculateNextExecTime()
			
			// 如果是间隔任务，重新加入调度器
			if task.Type != models.TaskTypeOnce {
				e.scheduler.AddTask(task)
			} else {
				task.Status = models.TaskStatusFinished
			}
		}
	} else {
		// 执行失败，如果是间隔任务，仍然计算下次执行时间
		if task.Type != models.TaskTypeOnce {
			task.LastExecTime = time.Now().Unix()
			task.NextExecTime = task.CalculateNextExecTime()
			e.scheduler.AddTask(task)
		} else {
			task.Status = models.TaskStatusFinished
		}
	}

	// 保存任务状态
	database.SaveTask(task)
}

// doRequest 执行 HTTP 请求，返回状态码、响应体和执行命令
func (e *Executor) doRequest(task *models.TimerTask) (int, string, string, error) {
	var body io.Reader
	if task.HTTPBody != "" {
		body = bytes.NewBufferString(task.HTTPBody)
	}

	req, err := http.NewRequestWithContext(
		context.Background(),
		string(task.HTTPMethod),
		task.HTTPURL,
		body,
	)
	if err != nil {
		return 0, "", "", err
	}

	// 设置请求头
	contentTypeSet := false
	for key, value := range task.HTTPHeaders {
		req.Header.Set(key, value)
		if key == "Content-Type" {
			contentTypeSet = true
		}
	}

	// 如果没有设置 Content-Type 且有请求体，默认设置为 application/json
	if !contentTypeSet && task.HTTPBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// 构建执行命令字符串
	execCommand := fmt.Sprintf("%s %s", task.HTTPMethod, task.HTTPURL)
	if task.HTTPBody != "" {
		execCommand += fmt.Sprintf(" --body '%s'", truncateString(task.HTTPBody, 200))
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return 0, "", execCommand, err
	}
	defer resp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", execCommand, nil
	}

	return resp.StatusCode, string(respBody), execCommand, nil
}

// executeScript 执行脚本代码，返回状态码、输出和执行命令
func (e *Executor) executeScript(task *models.TimerTask) (int, string, string, error) {
	if task.ScriptCode == "" {
		return 0, "", "", fmt.Errorf("脚本代码为空")
	}

	// 创建脚本目录
	scriptDir := "/app/scripts"
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		return 0, "", "", fmt.Errorf("创建脚本目录失败: %v", err)
	}

	// 根据脚本语言确定文件扩展名和解释器
	var ext, interpreter string
	switch task.ScriptLanguage {
	case models.ScriptLanguageJS:
		ext = ".js"
		interpreter = "node"
	case models.ScriptLanguagePython:
		ext = ".py"
		interpreter = "python3"
	case models.ScriptLanguageShell:
		ext = ".sh"
		interpreter = "sh"
	default:
		return 0, "", "", fmt.Errorf("不支持的脚本语言: %s", task.ScriptLanguage)
	}

	// 创建临时脚本文件
	scriptFile := filepath.Join(scriptDir, fmt.Sprintf("%s%s", task.Key, ext))
	if err := os.WriteFile(scriptFile, []byte(task.ScriptCode), 0644); err != nil {
		return 0, "", "", fmt.Errorf("写入脚本文件失败: %v", err)
	}
	defer os.Remove(scriptFile)

	// 构建执行命令字符串
	execCommand := fmt.Sprintf("%s %s", interpreter, scriptFile)

	// 最多尝试3次（首次执行 + 2次自动安装依赖后重试）
	maxAttempts := 3
	var lastOutput string
	var lastErr error
	var allCommands []string

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 执行脚本
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		
		cmd := exec.CommandContext(ctx, interpreter, scriptFile)
		cmd.Dir = "/app"
		
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		lastOutput = stdout.String()
		if stderr.Len() > 0 {
			lastOutput += "\n[stderr]: " + stderr.String()
		}

		if err == nil {
			cancel()
			return 200, lastOutput, execCommand, nil
		}

		if ctx.Err() == context.DeadlineExceeded {
			cancel()
			return 0, lastOutput, execCommand, fmt.Errorf("脚本执行超时")
		}
		cancel()

		// 检查是否是模块缺失错误，尝试自动安装
		missingModule := e.detectMissingModule(lastOutput, task.ScriptLanguage)
		if missingModule == "" {
			// 不是模块缺失错误，直接返回
			return 0, lastOutput, execCommand, err
		}

		// 尝试安装缺失的模块
		installCmd, installOutput, installErr := e.installModule(missingModule, task.ScriptLanguage)
		allCommands = append(allCommands, installCmd)
		
		if installErr != nil {
			fullCommand := execCommand + "\n" + strings.Join(allCommands, "\n")
			return 0, lastOutput + "\n[自动安装失败]: " + installOutput, fullCommand, fmt.Errorf("自动安装依赖失败: %v", installErr)
		}

		lastOutput += "\n[自动安装依赖]: " + missingModule + " - " + installOutput
		lastErr = err
	}

	// 合并所有执行的命令
	fullCommand := execCommand
	if len(allCommands) > 0 {
		fullCommand += "\n" + strings.Join(allCommands, "\n")
	}

	return 0, lastOutput, fullCommand, lastErr
}

// detectMissingModule 检测缺失的模块名
func (e *Executor) detectMissingModule(output string, language models.ScriptLanguage) string {
	switch language {
	case models.ScriptLanguagePython:
		// Python: ModuleNotFoundError: No module named 'xxx' 或 ImportError: No module named xxx
		if strings.Contains(output, "ModuleNotFoundError") || strings.Contains(output, "ImportError") {
			// 提取模块名 - 使用正则表达式更可靠
			// 匹配 "No module named 'xxx'" 或 "No module named \"xxx\"" 或 "No module named xxx"
			for _, quote := range []string{"'", "\"", ""} {
				pattern := "No module named " + quote
				if idx := strings.Index(output, pattern); idx != -1 {
					start := idx + len(pattern)
					rest := output[start:]
					// 查找结束位置
					var endIdx int
					if quote != "" {
						// 有引号，找匹配的引号
						endIdx = strings.Index(rest, quote)
					} else {
						// 无引号，找空格或换行
						endIdx = strings.IndexAny(rest, " \n\t")
					}
					if endIdx > 0 {
						moduleName := strings.TrimSpace(rest[:endIdx])
						// 映射常见的 import 名称到 PyPI 包名
						return mapPythonModuleToPackage(moduleName)
					}
				}
			}
		}
	case models.ScriptLanguageJS:
		// Node.js: Error: Cannot find module 'xxx' 或 Error: Cannot find module 'xxx'
		if strings.Contains(output, "Cannot find module") {
			if idx := strings.Index(output, "Cannot find module '"); idx != -1 {
				start := idx + len("Cannot find module '")
				rest := output[start:]
				if endIdx := strings.Index(rest, "'"); endIdx != -1 {
					return rest[:endIdx]
				}
			}
			if idx := strings.Index(output, "Cannot find module \""); idx != -1 {
				start := idx + len("Cannot find module \"")
				rest := output[start:]
				if endIdx := strings.Index(rest, "\""); endIdx != -1 {
					return rest[:endIdx]
				}
			}
		}
	}
	return ""
}

// mapPythonModuleToPackage 将 Python import 名称映射到 PyPI 包名
func mapPythonModuleToPackage(moduleName string) string {
	// 常见的 import 名称与 PyPI 包名不一致的映射（只保留转换不明显的）
	commonMappings := map[string]string{
		"PIL":      "Pillow",
		"cv2":      "opencv-python",
		"sklearn":  "scikit-learn",
		"bs4":      "beautifulsoup4",
		"dateutil": "python-dateutil",
		"yaml":     "PyYAML",
		"crypto":   "pycryptodome",
		"Crypto":   "pycryptodome",
		"Image":    "Pillow",
		"google":   "google-api-python-client",
		"hydra":    "hydra-core",
	}

	// 先检查常见映射
	if packageName, ok := commonMappings[moduleName]; ok {
		return packageName
	}

	// 尝试使用 PyPI JSON API 验证包名
	// 常见的包名转换规则
	candidates := []string{
		moduleName,                          // 原始名称
		strings.ToLower(moduleName),         // 小写
		strings.ReplaceAll(moduleName, "_", "-"), // 下划线转连字符
	}

	// 对于某些模块，尝试添加常见前缀/后缀
	if !strings.Contains(moduleName, "-") {
		candidates = append(candidates,
			"python-"+strings.ToLower(moduleName),
			"py"+strings.ToLower(moduleName),
			strings.ToLower(moduleName)+"-py",
		)
	}

	// 检查 PyPI 上是否存在该包
	for _, candidate := range candidates {
		if checkPyPIPackage(candidate) {
			return candidate
		}
	}

	// 如果都找不到，返回原始名称
	return moduleName
}

// checkPyPIPackage 检查 PyPI 上是否存在该包
func checkPyPIPackage(packageName string) bool {
	// 使用 PyPI JSON API 检查包是否存在
	url := fmt.Sprintf("https://pypi.org/pypi/%s/json", packageName)
	
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	
	// 200 表示包存在，404 表示不存在
	return resp.StatusCode == 200
}

// installModule 安装缺失的模块，返回执行的命令、输出和错误
func (e *Executor) installModule(moduleName string, language models.ScriptLanguage) (string, string, error) {
	switch language {
	case models.ScriptLanguagePython:
		return e.installPythonModule(moduleName)
	case models.ScriptLanguageJS:
		return e.installNPMModule(moduleName)
	default:
		return "", "", fmt.Errorf("不支持的语言")
	}
}

// installPythonModule 安装 Python 模块
func (e *Executor) installPythonModule(moduleName string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	execCommand := fmt.Sprintf("pip install --no-cache-dir --break-system-packages --root-user-action=ignore %s", moduleName)
	cmd := exec.CommandContext(ctx, "/usr/bin/pip", "install", "--no-cache-dir", "--break-system-packages", "--root-user-action=ignore", moduleName)
	cmd.Dir = "/app"

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n[stderr]: " + stderr.String()
	}

	if err != nil {
		return execCommand, output, err
	}
	return execCommand, output + "\n安装成功", nil
}

// installNPMModule 安装 npm 模块，自动检测并安装系统依赖
func (e *Executor) installNPMModule(moduleName string) (string, string, error) {
	var allCommands []string
	var allOutput strings.Builder

	// 最多尝试 3 次：首次安装 -> 安装基础编译工具 -> 安装检测到的特定依赖
	maxAttempts := 3
	installedDeps := make(map[string]bool)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		
		cmd := exec.CommandContext(ctx, "npm", "install", "-g", moduleName)
		cmd.Dir = "/app"
		
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		output := stdout.String()
		if stderr.Len() > 0 {
			output += "\n[stderr]: " + stderr.String()
		}
		allOutput.WriteString(output)
		allCommands = append(allCommands, fmt.Sprintf("npm install -g %s", moduleName))

		if err == nil {
			cancel()
			return strings.Join(allCommands, "\n"), allOutput.String() + "\n安装成功", nil
		}

		cancel()

		// 检测需要安装的系统依赖
		deps := e.detectNPMSystemDeps(output)
		if len(deps) == 0 {
			// 无法检测到系统依赖，尝试安装基础编译工具
			if attempt == 1 {
				deps = []string{"build-base", "python3"}
			} else {
				// 已经尝试过基础工具，仍然失败
				return strings.Join(allCommands, "\n"), allOutput.String(), err
			}
		}

		// 过滤已安装的依赖
		var newDeps []string
		for _, dep := range deps {
			if !installedDeps[dep] {
				newDeps = append(newDeps, dep)
				installedDeps[dep] = true
			}
		}

		if len(newDeps) == 0 {
			return strings.Join(allCommands, "\n"), allOutput.String(), err
		}

		// 安装系统依赖
		depCtx, depCancel := context.WithTimeout(context.Background(), 120*time.Second)
		depCmd := exec.CommandContext(depCtx, "apk", append([]string{"add", "--no-cache"}, newDeps...)...)
		depCmd.Dir = "/app"
		
		var depOut, depErr bytes.Buffer
		depCmd.Stdout = &depOut
		depCmd.Stderr = &depErr
		
		depErrResult := depCmd.Run()
		depCancel()
		
		depOutput := depOut.String()
		if depErr.Len() > 0 {
			depOutput += "\n" + depErr.String()
		}
		
		allCommands = append(allCommands, fmt.Sprintf("apk add --no-cache %s", strings.Join(newDeps, " ")))
		allOutput.WriteString(fmt.Sprintf("\n[系统依赖]: %s\n%s\n", strings.Join(newDeps, " "), depOutput))

		if depErrResult != nil {
			return strings.Join(allCommands, "\n"), allOutput.String(), fmt.Errorf("安装系统依赖失败: %v", depErrResult)
		}
	}

	return strings.Join(allCommands, "\n"), allOutput.String(), fmt.Errorf("安装失败，已尝试多次")
}

// detectNPMSystemDeps 从 npm install 错误输出中动态检测需要的系统依赖
func (e *Executor) detectNPMSystemDeps(output string) []string {
	// 常见 npm 模块的预设系统依赖（优先使用）
	presetDeps := map[string][]string{
		"canvas":    {"build-base", "python3", "cairo-dev", "pango-dev", "jpeg-dev", "giflib-dev", "librsvg-dev", "pixman-dev"},
		"sharp":     {"build-base", "python3", "vips-dev"},
		"bcrypt":    {"build-base", "python3"},
		"node-gyp":  {"build-base", "python3"},
		"node-canvas": {"build-base", "python3", "cairo-dev", "pango-dev", "jpeg-dev", "giflib-dev", "librsvg-dev", "pixman-dev"},
		"node-sass": {"build-base", "python3"},
		"sass":      {"build-base", "python3"},
	}

	// 检查输出中是否包含这些模块名
	outputLower := strings.ToLower(output)
	for moduleName, deps := range presetDeps {
		if strings.Contains(outputLower, strings.ToLower(moduleName)) {
			return deps
		}
	}

	// 从错误输出中提取缺失的库名
	libNames := extractMissingLibNames(output)
	if len(libNames) == 0 {
		return nil
	}

	var deps []string
	seen := make(map[string]bool)

	for _, libName := range libNames {
		// 动态搜索对应的 Alpine 包
		packages := e.searchAlpinePackages(libName)
		for _, pkg := range packages {
			if !seen[pkg] {
				seen[pkg] = true
				deps = append(deps, pkg)
			}
		}
	}

	return deps
}

// extractMissingLibNames 从错误输出中提取缺失的库名
func extractMissingLibNames(output string) []string {
	var libNames []string
	seen := make(map[string]bool)

	// 常见模式：
	// 1. fatal error: xxx.h: No such file or directory
	// 2. Cannot find xxx.h
	// 3. cannot find -lxxx
	// 4. xxx.so: cannot open shared object file
	// 5. Package 'xxx' not found (pkg-config)

	// 模式1: 头文件缺失
	headerPattern := regexp.MustCompile(`(?i)(?:fatal error:\s*|Cannot find\s*)([\w-]+\.h)`)
	if matches := headerPattern.FindAllStringSubmatch(output, -1); matches != nil {
		for _, m := range matches {
			headerFile := m[1]
			// 提取库名：cairo.h -> cairo, jpeglib.h -> jpeg
			libName := extractLibNameFromHeader(headerFile)
			if libName != "" && !seen[libName] {
				seen[libName] = true
				libNames = append(libNames, libName)
			}
		}
	}

	// 模式2: 链接库缺失 -lxxx
	linkPattern := regexp.MustCompile(`(?i)cannot find -l([\w-]+)`)
	if matches := linkPattern.FindAllStringSubmatch(output, -1); matches != nil {
		for _, m := range matches {
			libName := m[1]
			if !seen[libName] {
				seen[libName] = true
				libNames = append(libNames, libName)
			}
		}
	}

	// 模式3: pkg-config 找不到包
	pkgConfigPattern := regexp.MustCompile(`(?i)Package\s+'([^']+)'\s+not found`)
	if matches := pkgConfigPattern.FindAllStringSubmatch(output, -1); matches != nil {
		for _, m := range matches {
			libName := m[1]
			if !seen[libName] {
				seen[libName] = true
				libNames = append(libNames, libName)
			}
		}
	}

	// 模式4: .pc 文件缺失
	pcPattern := regexp.MustCompile(`(?i)Could not find ([\w-]+\.pc)`)
	if matches := pcPattern.FindAllStringSubmatch(output, -1); matches != nil {
		for _, m := range matches {
			pcFile := m[1]
			libName := strings.TrimSuffix(pcFile, ".pc")
			if !seen[libName] {
				seen[libName] = true
				libNames = append(libNames, libName)
			}
		}
	}

	return libNames
}

// extractLibNameFromHeader 从头文件名提取库名
func extractLibNameFromHeader(headerFile string) string {
	// 去掉 .h 后缀
	name := strings.TrimSuffix(headerFile, ".h")
	name = strings.ToLower(name)

	// 特殊映射
	specialCases := map[string]string{
		"jpeglib":      "jpeg",
		"jerror":       "jpeg",
		"jmorecfg":     "jpeg",
		"pnglibconf":   "png",
		"png":          "png",
		"gif_lib":      "giflib",
		"tiffconf":     "tiff",
		"tiff":         "tiff",
		"webp":         "webp",
		"rsvg":         "librsvg",
		"freetype":     "freetype2",
		"ft2build":     "freetype2",
		"expat":        "expat",
		"zlib":         "zlib",
		"bzlib":        "bzip2",
		"sqlite3":      "sqlite3",
		"openssl":      "openssl",
		"ssl":          "openssl",
		"crypto":       "openssl",
		"curl":         "libcurl",
		"libxml":       "libxml-2.0",
		"libxml2":      "libxml-2.0",
		"icu":          "icu-uc",
		"unicode":      "icu-uc",
		"fontconfig":   "fontconfig",
		"pango":        "pango",
		"cairo":        "cairo",
		"pixman":       "pixman-1",
		"glib":         "glib-2.0",
		"gio":          "glib-2.0",
		"gobject":      "glib-2.0",
		"vips":         "vips",
	}

	if mapped, ok := specialCases[name]; ok {
		return mapped
	}

	return name
}

// searchAlpinePackages 动态搜索 Alpine 包
func (e *Executor) searchAlpinePackages(libName string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 特殊处理一些常见情况
	switch libName {
	case "python", "python3":
		return []string{"python3"}
	case "make", "gcc", "g++":
		return []string{"build-base"}
	}

	// 先更新 apk 索引（静默模式）
	updateCmd := exec.CommandContext(ctx, "apk", "update", "--quiet")
	updateCmd.Dir = "/app"
	updateCmd.Run() // 忽略错误，可能已经更新过

	// 构建搜索候选列表
	candidates := []string{
		libName + "-dev",
		libName,
		"lib" + libName + "-dev",
		"lib" + libName,
	}

	var foundPackages []string

	// 使用 apk search 搜索包
	for _, candidate := range candidates {
		cmd := exec.CommandContext(ctx, "apk", "search", "-q", candidate)
		cmd.Dir = "/app"
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			continue
		}

		result := strings.TrimSpace(out.String())
		if result == "" {
			continue
		}

		// apk search -q 只返回包名，不返回版本
		// 检查是否有精确匹配
		lines := strings.Split(result, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			// 精确匹配
			if line == candidate {
				foundPackages = append(foundPackages, candidate)
				break
			}
		}

		if len(foundPackages) > 0 {
			break
		}
	}

	// 去重
	seen := make(map[string]bool)
	var result []string
	for _, pkg := range foundPackages {
		if !seen[pkg] {
			seen[pkg] = true
			result = append(result, pkg)
		}
	}

	return result
}

// truncateString 截断字符串
func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ExecuteWithCallback 执行任务并回调
func (e *Executor) ExecuteWithCallback(task *models.TimerTask, callback func(*models.TimerTask, bool, error)) {
	var lastErr error
	var statusCode int
	var responseBody string
	var execCommand string
	var success bool
	retryCount := 0

	for i := 0; i <= task.MaxRetryCount; i++ {
		retryCount = i
		statusCode, responseBody, execCommand, lastErr = e.doRequest(task)
		if lastErr == nil && statusCode >= 200 && statusCode < 300 {
			success = true
			break
		}
		if i < task.MaxRetryCount {
			time.Sleep(time.Second * time.Duration(i+1))
		}
	}

	// 记录执行日志
	log := &models.TaskExecuteLog{
		TaskKey:     task.Key,
		ExecTime:    time.Now().Unix(),
		Success:     success,
		StatusCode:  statusCode,
		RetryCount:  retryCount,
		ExecCommand: execCommand,
	}
	
	if lastErr != nil {
		log.Message = fmt.Sprintf("请求错误: %s", lastErr.Error())
	} else if !success {
		log.Message = fmt.Sprintf("HTTP %d - %s", statusCode, truncateString(responseBody, 5000))
	} else {
		log.Message = fmt.Sprintf("成功 - %s", truncateString(responseBody, 5000))
	}

	database.CreateExecuteLog(log)

	// 更新任务
	if success {
		task.ExecCount++
		task.LastExecTime = time.Now().Unix()
		if task.MaxExecCount > 0 && task.ExecCount >= task.MaxExecCount {
			task.Status = models.TaskStatusFinished
		} else {
			task.NextExecTime = task.CalculateNextExecTime()
			if task.Type != models.TaskTypeOnce {
				e.scheduler.AddTask(task)
			} else {
				task.Status = models.TaskStatusFinished
			}
		}
	} else {
		if task.Type != models.TaskTypeOnce {
			task.LastExecTime = time.Now().Unix()
			task.NextExecTime = task.CalculateNextExecTime()
			e.scheduler.AddTask(task)
		} else {
			task.Status = models.TaskStatusFinished
		}
	}

	database.SaveTask(task)

	if callback != nil {
		callback(task, success, lastErr)
	}
}

// MarshalHeaders 序列化请求头
func MarshalHeaders(headers map[string]string) string {
	if headers == nil || len(headers) == 0 {
		return ""
	}
	bs, _ := json.Marshal(headers)
	return string(bs)
}

// UnmarshalHeaders 反序列化请求头
func UnmarshalHeaders(data string) map[string]string {
	if data == "" {
		return nil
	}
	var headers map[string]string
	json.Unmarshal([]byte(data), &headers)
	return headers
}
