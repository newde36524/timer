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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	var execCommand string
	switch language {
	case models.ScriptLanguagePython:
		// 使用 pip 安装，添加 --break-system-packages 以支持 Alpine Linux 3.19+
		// 添加 --root-user-action=ignore 忽略 root 用户警告
		// 使用完整路径避免 PATH 问题
		cmd = exec.CommandContext(ctx, "/usr/bin/pip", "install", "--no-cache-dir", "--break-system-packages", "--root-user-action=ignore", moduleName)
		execCommand = fmt.Sprintf("pip install --no-cache-dir --break-system-packages --root-user-action=ignore %s", moduleName)
	case models.ScriptLanguageJS:
		// 使用 npm 安装到全局
		cmd = exec.CommandContext(ctx, "npm", "install", "-g", moduleName)
		execCommand = fmt.Sprintf("npm install -g %s", moduleName)
	default:
		return "", "", fmt.Errorf("不支持的语言")
	}

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

	return execCommand, "安装成功", nil
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
