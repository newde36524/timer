package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/newde36524/timer/api"
	"github.com/newde36524/timer/config"
	"github.com/newde36524/timer/database"
	"github.com/newde36524/timer/executor"
	"github.com/newde36524/timer/models"
	"github.com/newde36524/timer/scheduler"
)

func main() {
	// 加载配置
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 设置时区
	if cfg.Server.Timezone != "" {
		loc, err := time.LoadLocation(cfg.Server.Timezone)
		if err != nil {
			log.Printf("Warning: Failed to load timezone %s: %v, using local timezone", cfg.Server.Timezone, err)
		} else {
			time.Local = loc
			log.Printf("Timezone set to %s", cfg.Server.Timezone)
		}
	}

	// 初始化数据库
	if err := database.Init(&database.Config{
		Path:     cfg.Database.Path,
		LogLevel: cfg.Database.LogLevel,
	}); err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}
	log.Println("Database initialized")

	// 创建调度器
	sched := scheduler.NewScheduler(nil)

	// 创建执行器
	exec := executor.NewExecutor(sched)

	// 设置调度器的执行函数
	sched.SetExecutor(func(task *models.TimerTask) {
		exec.Execute(task)
	})

	// 加载现有任务
	tasks, err := database.LoadActiveTasks()
	if err != nil {
		log.Fatalf("Failed to load tasks: %v", err)
	}
	for _, task := range tasks {
		// 重新计算下次执行时间
		task.NextExecTime = task.CalculateNextExecTime()
		sched.AddTask(task)
	}
	log.Printf("Loaded %d active tasks", len(tasks))

	// 启动调度器
	sched.Start()
	log.Println("Scheduler started")

	// 启动日志清理定时任务（每天凌晨清理7天前的日志）
	go startLogCleanup(7)

	// 创建 API 处理器
	handler := api.NewHandler(sched)

	// 设置 Gin 模式
	gin.SetMode(cfg.Server.Mode)

	// 创建路由
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(corsMiddleware())

	// 静态文件
	router.Static("/static", "./web/static")
	router.StaticFile("/", "./web/index.html")

	// API 路由
	apiGroup := router.Group("/api")
	{
		// 用户认证（无需登录）
		apiGroup.POST("/register", handler.Register)
		apiGroup.POST("/login", handler.Login)

		// 需要认证的接口
		authGroup := apiGroup.Group("")
		authGroup.Use(api.AuthMiddleware())
		{
			// 用户信息
			authGroup.GET("/user", handler.GetCurrentUser)

			// 任务管理
			authGroup.POST("/tasks", handler.CreateTask)
			authGroup.GET("/tasks", handler.ListTasks)
			authGroup.GET("/tasks/:key", handler.GetTask)
			authGroup.PUT("/tasks/:key", handler.UpdateTask)
			authGroup.DELETE("/tasks/:key", handler.DeleteTask)
			authGroup.POST("/tasks/:key/pause", handler.PauseTask)
			authGroup.POST("/tasks/:key/resume", handler.ResumeTask)
			authGroup.GET("/tasks/:key/logs", handler.GetTaskLogs)

			// 工具
			authGroup.GET("/generate-key", handler.GenerateKey)
			authGroup.GET("/stats", handler.GetStats)
		}
	}

	// 启动服务器
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: router,
	}

	go func() {
		log.Printf("Server starting on port %d", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	sched.Stop()
	log.Println("Server stopped")
}

// corsMiddleware CORS 中间件
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// startLogCleanup 启动日志清理定时任务
func startLogCleanup(retentionDays int) {
	// 启动时先清理一次
	cleanupLogs(retentionDays)

	// 计算下次凌晨的时间
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		duration := next.Sub(now)

		select {
		case <-time.After(duration):
			cleanupLogs(retentionDays)
		}
	}
}

// cleanupLogs 清理过期日志
func cleanupLogs(days int) {
	if err := database.CleanupOldLogs(days); err != nil {
		log.Printf("Failed to cleanup old logs: %v", err)
	} else {
		log.Printf("Cleaned up logs older than %d days", days)
	}
}