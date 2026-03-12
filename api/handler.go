package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newde36524/timer/auth"
	"github.com/newde36524/timer/database"
	"github.com/newde36524/timer/models"
	"github.com/newde36524/timer/scheduler"
)

// Handler API 处理器
type Handler struct {
	scheduler *scheduler.Scheduler
}

// NewHandler 创建处理器
func NewHandler(s *scheduler.Scheduler) *Handler {
	return &Handler{scheduler: s}
}

// AuthMiddleware 认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "认证格式错误"})
			c.Abort()
			return
		}

		claims, err := auth.ParseToken(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "认证已过期，请重新登录"})
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Next()
	}
}

// GetUserID 从上下文获取用户ID
func GetUserID(c *gin.Context) uint {
	userID, _ := c.Get("user_id")
	return userID.(uint)
}

// Register 用户注册
func (h *Handler) Register(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required,min=3,max=50"`
		Password string `json:"password" binding:"required,min=6,max=100"`
		Nickname string `json:"nickname"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 检查用户名是否存在
	var count int64
	database.DB.Model(&models.User{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名已存在"})
		return
	}

	// 加密密码
	hashedPassword, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	user := &models.User{
		Username:  req.Username,
		Password:  hashedPassword,
		Nickname:  req.Nickname,
		CreateTime: time.Now().Unix(),
	}

	if err := database.DB.Create(user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "注册失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "注册成功"})
}

// Login 用户登录
func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user models.User
	if err := database.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	if !auth.CheckPassword(req.Password, user.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	token, err := auth.GenerateToken(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成令牌失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"nickname": user.Nickname,
		},
	})
}

// GetCurrentUser 获取当前用户信息
func (h *Handler) GetCurrentUser(c *gin.Context) {
	userID := GetUserID(c)
	var user models.User
	if err := database.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"nickname": user.Nickname,
		},
	})
}

// CreateTask 创建任务
func (h *Handler) CreateTask(c *gin.Context) {
	userID := GetUserID(c)

	var req models.TaskCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 检查 key 是否已存在
	var count int64
	database.DB.Model(&models.TimerTask{}).Where("key = ? AND is_deleted = 0", req.Key).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务 key 已存在"})
		return
	}

	// 设置默认值
	if req.MaxRetryCount <= 0 {
		req.MaxRetryCount = 3
	}
	if req.Group == "" {
		req.Group = "default"
	}
	if req.HTTPMethod == "" {
		req.HTTPMethod = models.HTTPMethodPost
	}

	// 创建任务
	task := &models.TimerTask{
		UserID:        userID,
		Name:          req.Name,
		Key:           req.Key,
		Type:          req.Type,
		Status:        models.TaskStatusActive,
		CreateTime:    time.Now().Unix(),
		StartTime:     req.StartTime,
		NextExecTime:  req.StartTime,
		Interval:      req.Interval,
		MaxRetryCount: req.MaxRetryCount,
		MaxExecCount:  req.MaxExecCount,
		HTTPMethod:    req.HTTPMethod,
		HTTPURL:       req.HTTPURL,
		HTTPHeaders:   req.HTTPHeaders,
		HTTPBody:      req.HTTPBody,
		Group:         req.Group,
	}

	// 计算首次执行时间
	task.NextExecTime = task.CalculateNextExecTime()

	// 保存到数据库
	if err := database.DB.Create(task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 添加到调度器
	h.scheduler.AddTask(task)

	c.JSON(http.StatusCreated, gin.H{
		"message": "任务创建成功",
		"data":    task.ToResponse(),
	})
}

// GetTask 获取任务详情
func (h *Handler) GetTask(c *gin.Context) {
	userID := GetUserID(c)
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务 key 不能为空"})
		return
	}

	var task models.TimerTask
	if err := database.DB.Where("key = ? AND user_id = ? AND is_deleted = 0", key, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": task.ToResponse()})
}

// ListTasks 获取任务列表
func (h *Handler) ListTasks(c *gin.Context) {
	userID := GetUserID(c)
	group := c.Query("group")
	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := database.DB.Model(&models.TimerTask{}).Where("user_id = ? AND is_deleted = 0", userID)

	if group != "" {
		query = query.Where("group = ?", group)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var tasks []*models.TimerTask
	offset := (page - 1) * pageSize
	if err := query.Order("next_exec_time ASC").Offset(offset).Limit(pageSize).Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responses := make([]*models.TaskResponse, len(tasks))
	for i, task := range tasks {
		responses[i] = task.ToResponse()
	}

	c.JSON(http.StatusOK, gin.H{
		"data": responses,
		"pagination": gin.H{
			"page":       page,
			"page_size":  pageSize,
			"total":      total,
			"total_page": (total + int64(pageSize) - 1) / int64(pageSize),
		},
	})
}

// UpdateTask 更新任务
func (h *Handler) UpdateTask(c *gin.Context) {
	userID := GetUserID(c)
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务 key 不能为空"})
		return
	}

	var req models.TaskUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var task models.TimerTask
	if err := database.DB.Where("key = ? AND user_id = ? AND is_deleted = 0", key, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	// 更新字段
	if req.Name != "" {
		task.Name = req.Name
	}
	if req.StartTime > 0 {
		task.StartTime = req.StartTime
		task.NextExecTime = task.StartTime
	}
	if req.Interval > 0 {
		task.Interval = req.Interval
	}
	if req.MaxRetryCount > 0 {
		task.MaxRetryCount = req.MaxRetryCount
	}
	if req.MaxExecCount >= 0 {
		task.MaxExecCount = req.MaxExecCount
	}
	if req.HTTPMethod != "" {
		task.HTTPMethod = req.HTTPMethod
	}
	if req.HTTPURL != "" {
		task.HTTPURL = req.HTTPURL
	}
	if req.HTTPHeaders != nil {
		task.HTTPHeaders = req.HTTPHeaders
	}
	if req.HTTPBody != "" {
		task.HTTPBody = req.HTTPBody
	}
	if req.Status > 0 {
		task.Status = req.Status
	}

	// 重新计算下次执行时间
	task.NextExecTime = task.CalculateNextExecTime()

	// 保存到数据库
	if err := database.DB.Save(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 更新调度器
	if task.Status == models.TaskStatusActive {
		h.scheduler.UpdateTask(&task)
	} else {
		h.scheduler.RemoveTask(task.Key)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "任务更新成功",
		"data":    task.ToResponse(),
	})
}

// DeleteTask 删除任务
func (h *Handler) DeleteTask(c *gin.Context) {
	userID := GetUserID(c)
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务 key 不能为空"})
		return
	}

	var task models.TimerTask
	if err := database.DB.Where("key = ? AND user_id = ? AND is_deleted = 0", key, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	// 软删除
	task.IsDeleted = 1
	task.Status = models.TaskStatusFinished
	if err := database.DB.Save(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 从调度器移除
	h.scheduler.RemoveTask(key)

	c.JSON(http.StatusOK, gin.H{"message": "任务删除成功"})
}

// PauseTask 暂停任务
func (h *Handler) PauseTask(c *gin.Context) {
	userID := GetUserID(c)
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务 key 不能为空"})
		return
	}

	var task models.TimerTask
	if err := database.DB.Where("key = ? AND user_id = ? AND is_deleted = 0", key, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	task.Status = models.TaskStatusPaused
	if err := database.DB.Save(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.scheduler.RemoveTask(key)

	c.JSON(http.StatusOK, gin.H{
		"message": "任务已暂停",
		"data":    task.ToResponse(),
	})
}

// ResumeTask 恢复任务
func (h *Handler) ResumeTask(c *gin.Context) {
	userID := GetUserID(c)
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务 key 不能为空"})
		return
	}

	var task models.TimerTask
	if err := database.DB.Where("key = ? AND user_id = ? AND is_deleted = 0", key, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	task.Status = models.TaskStatusActive
	task.NextExecTime = task.CalculateNextExecTime()
	if err := database.DB.Save(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.scheduler.AddTask(&task)

	c.JSON(http.StatusOK, gin.H{
		"message": "任务已恢复",
		"data":    task.ToResponse(),
	})
}

// GetTaskLogs 获取任务执行日志
func (h *Handler) GetTaskLogs(c *gin.Context) {
	userID := GetUserID(c)
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务 key 不能为空"})
		return
	}

	// 验证任务归属
	var task models.TimerTask
	if err := database.DB.Where("key = ? AND user_id = ? AND is_deleted = 0", key, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	logs, total, err := database.GetTaskLogsPaged(key, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": logs,
		"pagination": gin.H{
			"page":       page,
			"page_size":  pageSize,
			"total":      total,
			"total_page": (total + int64(pageSize) - 1) / int64(pageSize),
		},
	})
}

// GenerateKey 生成唯一 key
func (h *Handler) GenerateKey(c *gin.Context) {
	key := uuid.New().String()[:8]
	c.JSON(http.StatusOK, gin.H{"key": key})
}

// GetStats 获取统计信息
func (h *Handler) GetStats(c *gin.Context) {
	userID := GetUserID(c)

	var activeCount, pausedCount, finishedCount int64

	database.DB.Model(&models.TimerTask{}).Where("user_id = ? AND status = ? AND is_deleted = 0", userID, models.TaskStatusActive).Count(&activeCount)
	database.DB.Model(&models.TimerTask{}).Where("user_id = ? AND status = ? AND is_deleted = 0", userID, models.TaskStatusPaused).Count(&pausedCount)
	database.DB.Model(&models.TimerTask{}).Where("user_id = ? AND status = ? AND is_deleted = 0", userID, models.TaskStatusFinished).Count(&finishedCount)

	c.JSON(http.StatusOK, gin.H{
		"active_tasks":   activeCount,
		"paused_tasks":   pausedCount,
		"finished_tasks": finishedCount,
		"queue_size":     h.scheduler.Size(),
	})
}