package database

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/newde36524/timer/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB 全局数据库连接
var DB *gorm.DB

// Config 数据库配置
type Config struct {
	Path     string // SQLite 数据库文件路径
	LogLevel string // 日志级别: silent, error, warn, info
}

// 磁盘容量阈值（剩余空间小于 100MB 时触发清理）
const minDiskSpaceMB = 100

// Init 初始化数据库
func Init(cfg *Config) error {
	var err error
	
	// 设置日志级别
	var logLevel logger.LogLevel
	switch cfg.LogLevel {
	case "silent":
		logLevel = logger.Silent
	case "error":
		logLevel = logger.Error
	case "warn":
		logLevel = logger.Warn
	case "info":
		logLevel = logger.Info
	default:
		logLevel = logger.Warn
	}

	// 使用 glebarez/sqlite 驱动
	dsn := cfg.Path
	if dsn == "" {
		dsn = "timer.db"
	}

	DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	// 自动迁移
	err = DB.AutoMigrate(&models.User{}, &models.TimerTask{}, &models.TaskExecuteLog{})
	if err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}

// LoadActiveTasks 加载所有活动任务
func LoadActiveTasks() ([]*models.TimerTask, error) {
	var tasks []*models.TimerTask
	err := DB.Where("status = ? AND is_deleted = ?", models.TaskStatusActive, 0).
		Order("next_exec_time ASC").
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetUserByUsername 根据用户名获取用户
func GetUserByUsername(username string) (*models.User, error) {
	var user models.User
	err := DB.Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByID 根据 ID 获取用户
func GetUserByID(id uint) (*models.User, error) {
	var user models.User
	err := DB.First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateTaskStatus 更新任务状态
func UpdateTaskStatus(id uint, status models.TaskStatus) error {
	return DB.Model(&models.TimerTask{}).Where("id = ?", id).Update("status", status).Error
}

// SaveTask 保存任务
func SaveTask(task *models.TimerTask) error {
	return DB.Save(task).Error
}

// 每个任务保留的最大日志数量
const maxLogsPerTask = 100

// CreateExecuteLog 创建执行日志（带磁盘容量检查和日志数量限制）
func CreateExecuteLog(log *models.TaskExecuteLog) error {
	// 检查磁盘容量
	if !checkDiskSpace() {
		// 磁盘空间不足，清理最早的日志
		cleanupOldestLogs(100) // 每次清理100条
	}
	
	// 创建日志
	if err := DB.Create(log).Error; err != nil {
		return err
	}
	
	// 检查该任务的日志数量，超过限制则删除最旧的
	cleanupTaskLogs(log.TaskKey, maxLogsPerTask)
	
	return nil
}

// cleanupTaskLogs 清理指定任务的旧日志，只保留最新的 count 条
func cleanupTaskLogs(taskKey string, maxCount int) error {
	// 统计该任务的日志总数
	var total int64
	if err := DB.Model(&models.TaskExecuteLog{}).Where("task_key = ?", taskKey).Count(&total).Error; err != nil {
		return err
	}
	
	// 如果不超过限制，无需清理
	if total <= int64(maxCount) {
		return nil
	}
	
	// 计算需要删除的数量
	deleteCount := total - int64(maxCount)
	
	// 获取需要删除的日志 ID（最旧的 deleteCount 条）
	var ids []uint
	if err := DB.Model(&models.TaskExecuteLog{}).
		Where("task_key = ?", taskKey).
		Order("exec_time ASC").
		Limit(int(deleteCount)).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	
	if len(ids) == 0 {
		return nil
	}
	
	return DB.Where("id IN ?", ids).Delete(&models.TaskExecuteLog{}).Error
}

// GetTaskLogs 获取任务执行日志
func GetTaskLogs(taskKey string, limit int) ([]*models.TaskExecuteLog, error) {
	var logs []*models.TaskExecuteLog
	query := DB.Where("task_key = ?", taskKey).Order("exec_time DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&logs).Error
	return logs, err
}

// GetTaskLogsPaged 分页获取任务执行日志（倒序）
func GetTaskLogsPaged(taskKey string, page, pageSize int) ([]*models.TaskExecuteLog, int64, error) {
	var logs []*models.TaskExecuteLog
	var total int64

	// 统计总数
	if err := DB.Model(&models.TaskExecuteLog{}).Where("task_key = ?", taskKey).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询（倒序）
	offset := (page - 1) * pageSize
	err := DB.Where("task_key = ?", taskKey).
		Order("exec_time DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&logs).Error

	return logs, total, err
}

// CleanupOldLogs 清理旧日志
func CleanupOldLogs(days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	return DB.Where("exec_time < ?", cutoff).Delete(&models.TaskExecuteLog{}).Error
}

// checkDiskSpace 检查磁盘剩余空间是否充足
func checkDiskSpace() bool {
	var stat syscall.Statfs_t
	wd, err := os.Getwd()
	if err != nil {
		return true // 无法获取路径时假设空间充足
	}
	
	if err := syscall.Statfs(wd, &stat); err != nil {
		return true // 无法获取磁盘信息时假设空间充足
	}
	
	// 计算剩余空间（MB）
	freeSpaceMB := (stat.Bavail * uint64(stat.Bsize)) / (1024 * 1024)
	return freeSpaceMB >= minDiskSpaceMB
}

// cleanupOldestLogs 清理最早的日志
func cleanupOldestLogs(count int) error {
	// 获取最早的 count 条日志的 ID
	var ids []uint
	if err := DB.Model(&models.TaskExecuteLog{}).
		Order("exec_time ASC").
		Limit(count).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	
	if len(ids) == 0 {
		return nil
	}
	
	return DB.Where("id IN ?", ids).Delete(&models.TaskExecuteLog{}).Error
}
