package scheduler

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/newde36524/timer/models"
)

// TaskItem 任务项，用于优先级队列
type TaskItem struct {
	task      *models.TimerTask
	index     int
	priority  int64 // 下次执行时间作为优先级
}

// PriorityQueue 优先级队列
type PriorityQueue []*TaskItem

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// 执行时间越早，优先级越高
	return pq[i].priority < pq[j].priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*TaskItem)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // 避免内存泄漏
	item.index = -1 // 标记为已移除
	*pq = old[0 : n-1]
	return item
}

// Scheduler 任务调度器
type Scheduler struct {
	pq       PriorityQueue
	mu       sync.RWMutex
	taskMap  map[string]*TaskItem // key -> TaskItem 映射
	notify   chan struct{}        // 通知有新任务加入
	ctx      context.Context
	cancel   context.CancelFunc
	executor func(*models.TimerTask) // 任务执行函数
}

// NewScheduler 创建调度器
func NewScheduler(executor func(*models.TimerTask)) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		pq:       make(PriorityQueue, 0),
		taskMap:  make(map[string]*TaskItem),
		notify:   make(chan struct{}, 1),
		ctx:      ctx,
		cancel:   cancel,
		executor: executor,
	}
}

// SetExecutor 设置执行函数
func (s *Scheduler) SetExecutor(executor func(*models.TimerTask)) {
	s.executor = executor
}

// AddTask 添加任务
func (s *Scheduler) AddTask(task *models.TimerTask) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 如果任务已存在，先移除
	if item, exists := s.taskMap[task.Key]; exists {
		heap.Remove(&s.pq, item.index)
	}

	// 创建新的任务项
	item := &TaskItem{
		task:     task,
		priority: task.NextExecTime,
	}
	heap.Push(&s.pq, item)
	s.taskMap[task.Key] = item

	// 通知有新任务
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// RemoveTask 移除任务
func (s *Scheduler) RemoveTask(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if item, exists := s.taskMap[key]; exists {
		heap.Remove(&s.pq, item.index)
		delete(s.taskMap, key)
	}
}

// UpdateTask 更新任务
func (s *Scheduler) UpdateTask(task *models.TimerTask) {
	s.AddTask(task) // 添加会自动处理已存在的任务
}

// GetNextTask 获取下一个要执行的任务（非阻塞）
func (s *Scheduler) GetNextTask() *models.TimerTask {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.pq.Len() == 0 {
		return nil
	}

	item := s.pq[0]
	return item.task
}

// PopTask 弹出并返回下一个要执行的任务
func (s *Scheduler) PopTask() *models.TimerTask {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pq.Len() == 0 {
		return nil
	}

	item := heap.Pop(&s.pq).(*TaskItem)
	delete(s.taskMap, item.task.Key)
	return item.task
}

// PeekNextExecTime 查看最近任务的执行时间
func (s *Scheduler) PeekNextExecTime() (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.pq.Len() == 0 {
		return 0, false
	}

	return s.pq[0].priority, true
}

// Size 返回队列大小
func (s *Scheduler) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pq.Len()
}

// Start 启动调度器
func (s *Scheduler) Start() {
	go s.run()
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	s.cancel()
}

// run 调度器主循环
func (s *Scheduler) run() {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.notify:
			// 有新任务加入，重置定时器
			s.resetTimer(timer)
		case <-timer.C:
			// 定时器触发，检查是否有任务需要执行
			s.processDueTasks()
			s.resetTimer(timer)
		}
	}
}

// resetTimer 重置定时器
func (s *Scheduler) resetTimer(timer *time.Timer) {
	nextTime, ok := s.PeekNextExecTime()
	if !ok {
		// 没有任务，设置一个较长的等待时间
		timer.Reset(time.Minute)
		return
	}

	now := time.Now().Unix()
	duration := time.Duration(nextTime-now) * time.Second

	if duration <= 0 {
		// 任务已到期，立即执行
		duration = time.Millisecond * 100
	} else if duration > time.Minute {
		// 最长等待一分钟
		duration = time.Minute
	}

	timer.Reset(duration)
}

// processDueTasks 处理到期的任务
func (s *Scheduler) processDueTasks() {
	now := time.Now().Unix()

	for {
		nextTime, ok := s.PeekNextExecTime()
		if !ok || nextTime > now {
			break
		}

		task := s.PopTask()
		if task == nil {
			break
		}

		// 检查任务状态
		if task.Status != models.TaskStatusActive || task.IsDeleted == 1 {
			continue
		}

		// 异步执行任务
		go s.executor(task)
	}
}

// GetAllTasks 获取所有任务（按执行时间排序）
func (s *Scheduler) GetAllTasks() []*models.TimerTask {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tasks := make([]*models.TimerTask, 0, s.pq.Len())
	for _, item := range s.pq {
		tasks = append(tasks, item.task)
	}
	return tasks
}
