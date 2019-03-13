package network

import (
	"crypto/tls"
	"github.com/gorilla/websocket"
	"github.com/name5566/leaf/log"
	"net"
	"net/http"
	"sync"
	"time"
)

// web socket 服务结构
type WSServer struct {
	Addr            string			// 地址
	MaxConnNum      int				// 最大链接数量
	PendingWriteNum int				// 等待写入
	MaxMsgLen       uint32			// 最大信息长度
	HTTPTimeout     time.Duration	// 链接超时时间
	CertFile        string			// 证书文件
	KeyFile         string			// 证书key
	NewAgent        func(*WSConn) Agent	// 创建代理
	ln              net.Listener		// 服务监听链接
	handler         *WSHandler			// 处理头结构
}

type WSHandler struct {
	maxConnNum      int
	pendingWriteNum int
	maxMsgLen       uint32
	newAgent        func(*WSConn) Agent
	upgrader        websocket.Upgrader
	conns           WebsocketConnSet		// 链接队列
	mutexConns      sync.Mutex
	wg              sync.WaitGroup
}

func (handler *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	conn, err := handler.upgrader.Upgrade(w, r, nil)	// 请求升级http 升级成一个websocket链接
	if err != nil {
		log.Debug("upgrade error: %v", err)
		return
	}
	conn.SetReadLimit(int64(handler.maxMsgLen))			// 设置读取限制

	handler.wg.Add(1)									// 等待一个组完成
	defer handler.wg.Done()

	handler.mutexConns.Lock()							// 同步锁
	if handler.conns == nil {							// 如果链接队列为空
		handler.mutexConns.Unlock()						// 解除同步锁
		conn.Close()									// 关闭链接
		return
	}
	if len(handler.conns) >= handler.maxConnNum {		// 如果链接列表中数量超出了设置的最大链接数
		handler.mutexConns.Unlock()						// 解除锁，并关闭链接
		conn.Close()
		log.Debug("too many connections")
		return
	}
	handler.conns[conn] = struct{}{}	// 链接 key val空结构
	handler.mutexConns.Unlock()			// 解除同步锁

	wsConn := newWSConn(conn, handler.pendingWriteNum, handler.maxMsgLen)	// 设置要发送信息的条数和最大长度
	agent := handler.newAgent(wsConn)
	agent.Run()

	// cleanup
	wsConn.Close()
	handler.mutexConns.Lock()
	delete(handler.conns, conn)
	handler.mutexConns.Unlock()
	agent.OnClose()
}

func (server *WSServer) Start() {
	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatal("%v", err)
	}

	if server.MaxConnNum <= 0 {
		server.MaxConnNum = 100
		log.Release("invalid MaxConnNum, reset to %v", server.MaxConnNum)
	}
	if server.PendingWriteNum <= 0 {
		server.PendingWriteNum = 100
		log.Release("invalid PendingWriteNum, reset to %v", server.PendingWriteNum)
	}
	if server.MaxMsgLen <= 0 {
		server.MaxMsgLen = 4096
		log.Release("invalid MaxMsgLen, reset to %v", server.MaxMsgLen)
	}
	if server.HTTPTimeout <= 0 {
		server.HTTPTimeout = 10 * time.Second
		log.Release("invalid HTTPTimeout, reset to %v", server.HTTPTimeout)
	}
	if server.NewAgent == nil {
		log.Fatal("NewAgent must not be nil")
	}

	if server.CertFile != "" || server.KeyFile != "" {
		config := &tls.Config{}
		config.NextProtos = []string{"http/1.1"}

		var err error
		config.Certificates = make([]tls.Certificate, 1)
		config.Certificates[0], err = tls.LoadX509KeyPair(server.CertFile, server.KeyFile)
		if err != nil {
			log.Fatal("%v", err)
		}

		ln = tls.NewListener(ln, config)
	}

	server.ln = ln
	server.handler = &WSHandler{
		maxConnNum:      server.MaxConnNum,
		pendingWriteNum: server.PendingWriteNum,
		maxMsgLen:       server.MaxMsgLen,
		newAgent:        server.NewAgent,
		conns:           make(WebsocketConnSet),
		upgrader: websocket.Upgrader{
			HandshakeTimeout: server.HTTPTimeout,
			CheckOrigin:      func(_ *http.Request) bool { return true },
		},
	}

	httpServer := &http.Server{
		Addr:           server.Addr,
		Handler:        server.handler,
		ReadTimeout:    server.HTTPTimeout,
		WriteTimeout:   server.HTTPTimeout,
		MaxHeaderBytes: 1024,
	}

	go httpServer.Serve(ln)
}

func (server *WSServer) Close() {
	server.ln.Close()

	server.handler.mutexConns.Lock()
	for conn := range server.handler.conns {
		conn.Close()
	}
	server.handler.conns = nil
	server.handler.mutexConns.Unlock()

	server.handler.wg.Wait()
}
