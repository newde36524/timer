package models

import (
	"database/sql/driver"
	"encoding/json"
)

// TaskType 任务类型
type TaskType int

const (
	TaskTypeOnce     TaskType = 1 // 指定时间点执行一次
	TaskTypeInterval TaskType = 2 // 间隔执行
)

// TaskStatus 任务状态
type TaskStatus int

const (
	TaskStatusActive   TaskStatus = 1 // 活动中
	TaskStatusPaused   TaskStatus = 2 // 已暂停
	TaskStatusFinished TaskStatus = 3 // 已完成
)

// ExecMode 执行模式
type ExecMode int

const (
	ExecModeHTTP   ExecMode = 1 // HTTP 请求
	ExecModeScript ExecMode = 2 // 脚本执行
)

// ScriptLanguage 脚本语言
type ScriptLanguage string

const (
	ScriptLanguageJS     ScriptLanguage = "javascript"
	ScriptLanguagePython ScriptLanguage = "python"
	ScriptLanguageShell  ScriptLanguage = "shell"
)

// HTTPMethod HTTP 请求方法
type HTTPMethod string

const (
	HTTPMethodGet    HTTPMethod = "GET"
	HTTPMethodPost   HTTPMethod = "POST"
	HTTPMethodPut    HTTPMethod = "PUT"
	HTTPMethodDelete HTTPMethod = "DELETE"
	HTTPMethodPatch  HTTPMethod = "PATCH"
)

// HTTPHeaders HTTP 请求头，存储为 JSON
type HTTPHeaders map[string]string

// Value 实现 driver.Valuer 接口
func (h HTTPHeaders) Value() (driver.Value, error) {
	if h == nil {
		return nil, nil
	}
	return json.Marshal(h)
}

// Scan 实现 sql.Scanner 接口
func (h *HTTPHeaders) Scan(value interface{}) error {
	if value == nil {
		*h = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(bytes, h)
}

// User 用户模型
type User struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Username  string `gorm:"type:varchar(50);uniqueIndex;not null" json:"username"`
	Password  string `gorm:"type:varchar(255);not null" json:"-"`
	Nickname  string `gorm:"type:varchar(100)" json:"nickname"`
	CreateTime int64 `gorm:"not null" json:"create_time"`
}

// TableName 指定表名
func (User) TableName() string {
	return "users"
}

// TimerTask 定时任务模型
type TimerTask struct {
	ID             uint          `gorm:"primaryKey" json:"id"`
	UserID         uint          `gorm:"index;not null" json:"user_id"`
	Name           string        `gorm:"type:varchar(255);not null" json:"name"`
	Key            string        `gorm:"type:varchar(255);uniqueIndex;not null" json:"key"`
	Type           TaskType      `gorm:"not null;default:1" json:"type"`
	Status         TaskStatus    `gorm:"not null;default:1" json:"status"`
	ExecMode       ExecMode      `gorm:"not null;default:1" json:"exec_mode"`                // 执行模式：1=HTTP, 2=脚本
	CreateTime     int64         `gorm:"not null" json:"create_time"`
	StartTime      int64         `gorm:"not null" json:"start_time"`
	NextExecTime   int64         `gorm:"index;not null" json:"next_exec_time"`
	LastExecTime   int64         `gorm:"not null;default:0" json:"last_exec_time"`
	Interval       int64         `gorm:"not null;default:0" json:"interval"`
	MaxRetryCount  int           `gorm:"not null;default:3" json:"max_retry_count"`
	ExecCount      int           `gorm:"not null;default:0" json:"exec_count"`
	MaxExecCount   int           `gorm:"not null;default:0" json:"max_exec_count"`
	HTTPMethod     HTTPMethod    `gorm:"type:varchar(10);not null;default:'POST'" json:"http_method"`
	HTTPURL        string        `gorm:"type:varchar(500)" json:"http_url"`
	HTTPHeaders    HTTPHeaders   `gorm:"type:text" json:"http_headers"`
	HTTPBody       string        `gorm:"type:text" json:"http_body"`
	ScriptLanguage ScriptLanguage `gorm:"type:varchar(20)" json:"script_language"`            // 脚本语言
	ScriptCode     string        `gorm:"type:longtext" json:"script_code"`                    // 脚本代码
	Group          string        `gorm:"type:varchar(100);not null;default:'default'" json:"group"`
	IsDeleted      int           `gorm:"not null;default:0" json:"is_deleted"`
}

// TableName 指定表名
func (TimerTask) TableName() string {
	return "timer_tasks"
}

// TaskExecuteLog 任务执行日志
type TaskExecuteLog struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	TaskKey     string `gorm:"type:varchar(255);index;not null" json:"task_key"`
	ExecTime    int64  `gorm:"not null" json:"exec_time"`
	Success     bool   `gorm:"not null" json:"success"`
	StatusCode  int    `gorm:"not null;default:0" json:"status_code"`
	Message     string `gorm:"type:text" json:"message"`
	RetryCount  int    `gorm:"not null;default:0" json:"retry_count"`
	ExecCommand string `gorm:"type:text" json:"exec_command"` // 执行的命令
}

// TableName 指定表名
func (TaskExecuteLog) TableName() string {
	return "task_execute_logs"
}

// TaskCreateRequest 创建任务请求
type TaskCreateRequest struct {
	Name           string            `json:"name" binding:"required"`
	Key            string            `json:"key" binding:"required"`
	Type           TaskType          `json:"type" binding:"required,oneof=1 2"`
	ExecMode       ExecMode          `json:"exec_mode" binding:"required,oneof=1 2"`       // 执行模式
	StartTime      int64             `json:"start_time" binding:"required"`                // Unix 时间戳
	Interval       int64             `json:"interval"`                                     // 秒
	MaxRetryCount  int               `json:"max_retry_count"`                              // 默认 3
	MaxExecCount   int               `json:"max_exec_count"`                               // 0 表示无限制
	HTTPMethod     HTTPMethod        `json:"http_method"`
	HTTPURL        string            `json:"http_url"`
	HTTPHeaders    map[string]string `json:"http_headers"`
	HTTPBody       string            `json:"http_body"`
	ScriptLanguage ScriptLanguage    `json:"script_language"`                              // 脚本语言
	ScriptCode     string            `json:"script_code"`                                  // 脚本代码
	Group          string            `json:"group"`                                        // 默认 "default"
}

// TaskUpdateRequest 更新任务请求
type TaskUpdateRequest struct {
	Name           string            `json:"name"`
	StartTime      int64             `json:"start_time"`
	Interval       int64             `json:"interval"`
	MaxRetryCount  int               `json:"max_retry_count"`
	MaxExecCount   int               `json:"max_exec_count"`
	HTTPMethod     HTTPMethod        `json:"http_method"`
	HTTPURL        string            `json:"http_url"`
	HTTPHeaders    map[string]string `json:"http_headers"`
	HTTPBody       string            `json:"http_body"`
	ScriptLanguage ScriptLanguage    `json:"script_language"`
	ScriptCode     string            `json:"script_code"`
	Status         TaskStatus        `json:"status"`
}

// TaskResponse 任务响应
type TaskResponse struct {
	ID             uint            `json:"id"`
	Name           string          `json:"name"`
	Key            string          `json:"key"`
	Type           TaskType        `json:"type"`
	TypeDesc       string          `json:"type_desc"`
	Status         TaskStatus      `json:"status"`
	StatusDesc     string          `json:"status_desc"`
	ExecMode       ExecMode        `json:"exec_mode"`
	ExecModeDesc   string          `json:"exec_mode_desc"`
	CreateTime     int64           `json:"create_time"`
	StartTime      int64           `json:"start_time"`
	NextExecTime   int64           `json:"next_exec_time"`
	LastExecTime   int64           `json:"last_exec_time"`
	Interval       int64           `json:"interval"`
	MaxRetryCount  int             `json:"max_retry_count"`
	ExecCount      int             `json:"exec_count"`
	MaxExecCount   int             `json:"max_exec_count"`
	HTTPMethod     HTTPMethod      `json:"http_method"`
	HTTPURL        string          `json:"http_url"`
	HTTPHeaders    HTTPHeaders     `json:"http_headers"`
	HTTPBody       string          `json:"http_body"`
	ScriptLanguage ScriptLanguage  `json:"script_language"`
	ScriptCode     string          `json:"script_code"`
	Group          string          `json:"group"`
}

// ToResponse 转换为响应格式
func (t *TimerTask) ToResponse() *TaskResponse {
	typeDesc := ""
	switch t.Type {
	case TaskTypeOnce:
		typeDesc = "指定时间点执行一次"
	case TaskTypeInterval:
		typeDesc = "间隔执行"
	}

	statusDesc := ""
	switch t.Status {
	case TaskStatusActive:
		statusDesc = "活动中"
	case TaskStatusPaused:
		statusDesc = "已暂停"
	case TaskStatusFinished:
		statusDesc = "已完成"
	}

	execModeDesc := ""
	switch t.ExecMode {
	case ExecModeHTTP:
		execModeDesc = "HTTP 请求"
	case ExecModeScript:
		execModeDesc = "脚本执行"
	}

	return &TaskResponse{
		ID:             t.ID,
		Name:           t.Name,
		Key:            t.Key,
		Type:           t.Type,
		TypeDesc:       typeDesc,
		Status:         t.Status,
		StatusDesc:     statusDesc,
		ExecMode:       t.ExecMode,
		ExecModeDesc:   execModeDesc,
		CreateTime:     t.CreateTime,
		StartTime:      t.StartTime,
		NextExecTime:   t.NextExecTime,
		LastExecTime:   t.LastExecTime,
		Interval:       t.Interval,
		MaxRetryCount:  t.MaxRetryCount,
		ExecCount:      t.ExecCount,
		MaxExecCount:   t.MaxExecCount,
		HTTPMethod:     t.HTTPMethod,
		HTTPURL:        t.HTTPURL,
		HTTPHeaders:    t.HTTPHeaders,
		HTTPBody:       t.HTTPBody,
		ScriptLanguage: t.ScriptLanguage,
		ScriptCode:     t.ScriptCode,
		Group:          t.Group,
	}
}

// CalculateNextExecTime 计算下次执行时间
func (t *TimerTask) CalculateNextExecTime() int64 {
	switch t.Type {
	case TaskTypeOnce:
		// 一次性任务，返回开始时间
		return t.StartTime
	case TaskTypeInterval:
		// 间隔执行
		if t.Interval <= 0 {
			return t.StartTime
		}
		if t.LastExecTime == 0 {
			return t.StartTime
		}
		return t.LastExecTime + t.Interval
	}
	
	return t.StartTime
}
