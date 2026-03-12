package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	var success bool
	retryCount := 0

	// 重试逻辑
	for i := 0; i <= task.MaxRetryCount; i++ {
		retryCount = i
		statusCode, responseBody, lastErr = e.doRequest(task)
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
		TaskKey:    task.Key,
		ExecTime:   time.Now().Unix(),
		Success:    success,
		StatusCode: statusCode,
		RetryCount: retryCount,
	}
	
	if lastErr != nil {
		log.Message = fmt.Sprintf("请求错误: %s", lastErr.Error())
	} else if !success {
		// 记录错误响应
		log.Message = fmt.Sprintf("HTTP %d - %s", statusCode, truncateString(responseBody, 5000))
	} else {
		// 记录成功响应
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

// doRequest 执行 HTTP 请求，返回状态码和响应体
func (e *Executor) doRequest(task *models.TimerTask) (int, string, error) {
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
		return 0, "", err
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

	resp, err := e.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", nil
	}

	return resp.StatusCode, string(respBody), nil
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
	var success bool
	retryCount := 0

	for i := 0; i <= task.MaxRetryCount; i++ {
		retryCount = i
		statusCode, responseBody, lastErr = e.doRequest(task)
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
		TaskKey:    task.Key,
		ExecTime:   time.Now().Unix(),
		Success:    success,
		StatusCode: statusCode,
		RetryCount: retryCount,
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
