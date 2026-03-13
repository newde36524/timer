// +build linux

package api

import (
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/newde36524/timer/auth"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// TerminalSession 终端会话
type TerminalSession struct {
	ws        *websocket.Conn
	pty       *os.File
	cmd       *exec.Cmd
	mutex     sync.Mutex
	closed    bool
	closeChan chan struct{}
}

// HandleTerminal 处理 WebSocket 终端连接
func HandleTerminal(c *gin.Context) {
	// 从 query 参数获取 token
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未授权"})
		return
	}

	// 验证 token
	claims, err := auth.ParseToken(token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "认证已过期"})
		return
	}
	_ = claims // 用户已验证

	// 升级为 WebSocket
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	session := &TerminalSession{
		ws:        ws,
		closeChan: make(chan struct{}),
	}

	// 启动 shell
	if err := session.startShell(); err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte("\x1b[31mError starting shell: "+err.Error()+"\x1b[0m\r\n"))
		ws.Close()
		return
	}

	// 处理连接
	session.handle()
}

// startShell 启动 shell 进程
func (s *TerminalSession) startShell() error {
	// 创建 PTY
	pty, tty, err := openPty()
	if err != nil {
		return err
	}

	// 设置终端大小
	setPtySize(pty, 120, 40)

	// 使用 bash 作为 shell
	cmd := exec.Command("/bin/bash")
	
	// 设置环境变量
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"HOME=/root",
	)
	cmd.Dir = "/app"
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	if err := cmd.Start(); err != nil {
		tty.Close()
		pty.Close()
		return err
	}

	s.pty = pty
	s.cmd = cmd
	tty.Close() // 父进程不需要 tty

	return nil
}

// handle 处理 WebSocket 消息
func (s *TerminalSession) handle() {
	defer s.close()

	// 从 PTY 读取输出并发送到 WebSocket
	go s.readPty()

	// 从 WebSocket 读取输入并发送到 PTY
	s.readWs()
}

// readPty 从 PTY 读取输出
func (s *TerminalSession) readPty() {
	buf := make([]byte, 4096)
	for {
		select {
		case <-s.closeChan:
			return
		default:
			n, err := s.pty.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("PTY read error: %v", err)
				}
				s.close()
				return
			}
			if n > 0 {
				s.mutex.Lock()
				if !s.closed {
					s.ws.WriteMessage(websocket.TextMessage, buf[:n])
				}
				s.mutex.Unlock()
			}
		}
	}
}

// readWs 从 WebSocket 读取输入
func (s *TerminalSession) readWs() {
	for {
		select {
		case <-s.closeChan:
			return
		default:
			_, msg, err := s.ws.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket read error: %v", err)
				}
				s.close()
				return
			}
			if len(msg) > 0 {
				s.mutex.Lock()
				if !s.closed {
					s.pty.Write(msg)
				}
				s.mutex.Unlock()
			}
		}
	}
}

// close 关闭会话
func (s *TerminalSession) close() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.closed {
		return
	}
	s.closed = true
	close(s.closeChan)

	if s.pty != nil {
		s.pty.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	if s.ws != nil {
		s.ws.Close()
	}
}

// openPty 打开 PTY
func openPty() (*os.File, *os.File, error) {
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}

	// 解锁 PTY
	var n uint32
	syscall.Syscall(syscall.SYS_IOCTL, pty.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&n)))

	// 获取从设备号
	sname, err := ptsname(pty)
	if err != nil {
		pty.Close()
		return nil, nil, err
	}

	// 设置权限
	if err := chmod(sname, 0620); err != nil {
		pty.Close()
		return nil, nil, err
	}

	// 打开从设备
	tty, err := os.OpenFile(sname, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		pty.Close()
		return nil, nil, err
	}

	return pty, tty, nil
}

// ptsname 获取 PTY 从设备名称
func ptsname(f *os.File) (string, error) {
	var n uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
	if errno != 0 {
		return "", errno
	}
	return "/dev/pts/" + itoa(int(n)), nil
}

// chmod 修改文件权限
func chmod(name string, mode uint32) error {
	return syscall.Chmod(name, mode)
}

// itoa 整数转字符串
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	n := i
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// setPtySize 设置 PTY 大小
func setPtySize(f *os.File, cols, rows int) error {
	var ws winsize
	ws.ws_col = uint16(cols)
	ws.ws_row = uint16(rows)
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

type winsize struct {
	ws_row    uint16
	ws_col    uint16
	ws_xpixel uint16
	ws_ypixel uint16
}
