package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// MinecraftServer represents a backend Minecraft server
type MinecraftServer struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Address      string            `json:"address"`
	Port         int               `json:"port"`
	Status       string            `json:"status"` // online, offline, starting, stopping
	PlayerCount  int               `json:"player_count"`
	MaxPlayers   int               `json:"max_players"`
	MOTD         string            `json:"motd"`
	Version      string            `json:"version"`
	LastPing     time.Time         `json:"last_ping"`
	Metadata     map[string]string `json:"metadata"`
	Priority     int               `json:"priority"` // for load balancing
}

// ProxyConfig represents the proxy configuration
type ProxyConfig struct {
	ListenPort     int               `json:"listen_port"`
	Servers        []MinecraftServer `json:"servers"`
	LoadBalancing  string            `json:"load_balancing"` // round_robin, least_connections, priority
	Maintenance    bool              `json:"maintenance"`
	MaintenanceMOTD string           `json:"maintenance_motd"`
	Whitelist      []string          `json:"whitelist"`
	Blacklist      []string          `json:"blacklist"`
	MaxConnections int               `json:"max_connections"`
}

// ConnectionStats represents connection statistics
type ConnectionStats struct {
	TotalConnections   int64     `json:"total_connections"`
	ActiveConnections  int64     `json:"active_connections"`
	BytesTransferred   int64     `json:"bytes_transferred"`
	LastConnection     time.Time `json:"last_connection"`
	ConnectionsPerHour int64     `json:"connections_per_hour"`
}

// MinecraftProxy represents the main proxy server
type MinecraftProxy struct {
	config       *ProxyConfig
	stats        *ConnectionStats
	connections  map[string]net.Conn
	serverIndex  int
	mu           sync.RWMutex
	listener     net.Listener
	apiServer    *gin.Engine
	upgrader     websocket.Upgrader
}

// NewMinecraftProxy creates a new Minecraft proxy
func NewMinecraftProxy() *MinecraftProxy {
	config := &ProxyConfig{
		ListenPort:      25565,
		Servers:         []MinecraftServer{},
		LoadBalancing:   "round_robin",
		Maintenance:     false,
		MaintenanceMOTD: "§cServer is under maintenance",
		Whitelist:       []string{},
		Blacklist:       []string{},
		MaxConnections:  1000,
	}

	stats := &ConnectionStats{
		TotalConnections:   0,
		ActiveConnections:  0,
		BytesTransferred:   0,
		LastConnection:     time.Now(),
		ConnectionsPerHour: 0,
	}

	proxy := &MinecraftProxy{
		config:      config,
		stats:       stats,
		connections: make(map[string]net.Conn),
		serverIndex: 0,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}

	proxy.setupAPI()
	return proxy
}

// setupAPI configures the REST API
func (p *MinecraftProxy) setupAPI() {
	p.apiServer = gin.Default()

	// CORS middleware
	p.apiServer.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		
		c.Next()
	})

	api := p.apiServer.Group("/api/v1")
	{
		// Configuration endpoints
		api.GET("/config", p.getConfig)
		api.PUT("/config", p.updateConfig)
		
		// Server management
		api.GET("/servers", p.getServers)
		api.POST("/servers", p.addServer)
		api.PUT("/servers/:id", p.updateServer)
		api.DELETE("/servers/:id", p.deleteServer)
		api.POST("/servers/:id/ping", p.pingServer)
		
		// Statistics
		api.GET("/stats", p.getStats)
		api.GET("/connections", p.getConnections)
		
		// Proxy control
		api.POST("/start", p.startProxy)
		api.POST("/stop", p.stopProxy)
		api.POST("/restart", p.restartProxy)
		
		// Health check
		api.GET("/health", p.healthCheck)
	}

	// WebSocket endpoint
	p.apiServer.GET("/ws", p.handleWebSocket)
}

// API Handlers
func (p *MinecraftProxy) getConfig(c *gin.Context) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c.JSON(http.StatusOK, p.config)
}

func (p *MinecraftProxy) updateConfig(c *gin.Context) {
	var newConfig ProxyConfig
	if err := c.ShouldBindJSON(&newConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	p.mu.Lock()
	p.config = &newConfig
	p.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{"message": "Configuration updated successfully"})
}

func (p *MinecraftProxy) getServers(c *gin.Context) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"servers": p.config.Servers})
}

func (p *MinecraftProxy) addServer(c *gin.Context) {
	var server MinecraftServer
	if err := c.ShouldBindJSON(&server); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if server.ID == "" {
		server.ID = fmt.Sprintf("server-%d", time.Now().UnixNano())
	}

	p.mu.Lock()
	p.config.Servers = append(p.config.Servers, server)
	p.mu.Unlock()

	c.JSON(http.StatusCreated, server)
}

func (p *MinecraftProxy) updateServer(c *gin.Context) {
	id := c.Param("id")
	var updatedServer MinecraftServer
	if err := c.ShouldBindJSON(&updatedServer); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for i, server := range p.config.Servers {
		if server.ID == id {
			updatedServer.ID = id
			p.config.Servers[i] = updatedServer
			c.JSON(http.StatusOK, updatedServer)
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "Server not found"})
}

func (p *MinecraftProxy) deleteServer(c *gin.Context) {
	id := c.Param("id")

	p.mu.Lock()
	defer p.mu.Unlock()

	for i, server := range p.config.Servers {
		if server.ID == id {
			p.config.Servers = append(p.config.Servers[:i], p.config.Servers[i+1:]...)
			c.JSON(http.StatusOK, gin.H{"message": "Server deleted successfully"})
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "Server not found"})
}

func (p *MinecraftProxy) pingServer(c *gin.Context) {
	id := c.Param("id")

	p.mu.RLock()
	var server *MinecraftServer
	for i, s := range p.config.Servers {
		if s.ID == id {
			server = &p.config.Servers[i]
			break
		}
	}
	p.mu.RUnlock()

	if server == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Server not found"})
		return
	}

	status := p.pingMinecraftServer(server)
	c.JSON(http.StatusOK, gin.H{"status": status})
}

func (p *MinecraftProxy) getStats(c *gin.Context) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c.JSON(http.StatusOK, p.stats)
}

func (p *MinecraftProxy) getConnections(c *gin.Context) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	connections := make([]string, 0, len(p.connections))
	for addr := range p.connections {
		connections = append(connections, addr)
	}
	
	c.JSON(http.StatusOK, gin.H{
		"active_connections": connections,
		"count": len(connections),
	})
}

func (p *MinecraftProxy) startProxy(c *gin.Context) {
	go p.Start()
	c.JSON(http.StatusOK, gin.H{"message": "Proxy started successfully"})
}

func (p *MinecraftProxy) stopProxy(c *gin.Context) {
	p.Stop()
	c.JSON(http.StatusOK, gin.H{"message": "Proxy stopped successfully"})
}

func (p *MinecraftProxy) restartProxy(c *gin.Context) {
	p.Stop()
	time.Sleep(1 * time.Second)
	go p.Start()
	c.JSON(http.StatusOK, gin.H{"message": "Proxy restarted successfully"})
}

func (p *MinecraftProxy) healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
		"uptime": time.Since(p.stats.LastConnection).Seconds(),
		"active_connections": p.stats.ActiveConnections,
	})
}

// WebSocket handler for real-time updates
func (p *MinecraftProxy) handleWebSocket(c *gin.Context) {
	conn, err := p.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.mu.RLock()
			data := gin.H{
				"type":        "update",
				"stats":       p.stats,
				"servers":     p.config.Servers,
				"connections": len(p.connections),
			}
			p.mu.RUnlock()

			if err := conn.WriteJSON(data); err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
		}
	}
}

// Start starts the Minecraft proxy
func (p *MinecraftProxy) Start() error {
	// Start API server
	go func() {
		log.Printf("Starting API server on :8080")
		if err := p.apiServer.Run(":8080"); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	// Start periodic server pinging
	go p.startServerPinging()

	// Start proxy listener
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", p.config.ListenPort))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %v", p.config.ListenPort, err)
	}

	p.listener = listener
	log.Printf("Minecraft proxy listening on port %d", p.config.ListenPort)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}

		go p.handleConnection(conn)
	}
}

// Stop stops the proxy
func (p *MinecraftProxy) Stop() {
	if p.listener != nil {
		p.listener.Close()
	}
}

// handleConnection handles a new client connection
func (p *MinecraftProxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	clientAddr := clientConn.RemoteAddr().String()
	log.Printf("New connection from %s", clientAddr)

	// Update statistics
	p.mu.Lock()
	p.stats.TotalConnections++
	p.stats.ActiveConnections++
	p.stats.LastConnection = time.Now()
	p.connections[clientAddr] = clientConn
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.stats.ActiveConnections--
		delete(p.connections, clientAddr)
		p.mu.Unlock()
	}()

	// Check if maintenance mode is enabled
	if p.config.Maintenance {
		p.sendMaintenanceResponse(clientConn)
		return
	}

	// Check connection limits
	if p.stats.ActiveConnections > int64(p.config.MaxConnections) {
		log.Printf("Connection limit exceeded for %s", clientAddr)
		return
	}

	// Check blacklist/whitelist
	if p.isBlacklisted(clientAddr) {
		log.Printf("Blacklisted connection from %s", clientAddr)
		return
	}

	// Select backend server
	server := p.selectServer()
	if server == nil {
		log.Printf("No available servers for %s", clientAddr)
		return
	}

	// Connect to backend server
	backendConn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", server.Address, server.Port))
	if err != nil {
		log.Printf("Failed to connect to backend %s:%d: %v", server.Address, server.Port, err)
		return
	}
	defer backendConn.Close()

	log.Printf("Proxying %s to %s:%d", clientAddr, server.Address, server.Port)

	// Start bidirectional copy
	done := make(chan bool, 2)

	go func() {
		defer func() { done <- true }()
		p.copyData(clientConn, backendConn)
	}()

	go func() {
		defer func() { done <- true }()
		p.copyData(backendConn, clientConn)
	}()

	<-done
}

// selectServer selects a backend server based on load balancing strategy
func (p *MinecraftProxy) selectServer() *MinecraftServer {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.config.Servers) == 0 {
		return nil
	}

	// Filter online servers
	onlineServers := make([]*MinecraftServer, 0)
	for i, server := range p.config.Servers {
		if server.Status == "online" {
			onlineServers = append(onlineServers, &p.config.Servers[i])
		}
	}

	if len(onlineServers) == 0 {
		return nil
	}

	switch p.config.LoadBalancing {
	case "round_robin":
		server := onlineServers[p.serverIndex%len(onlineServers)]
		p.serverIndex++
		return server
	case "least_connections":
		// Simple implementation - return first server
		return onlineServers[0]
	case "priority":
		// Return highest priority server
		var bestServer *MinecraftServer
		for _, server := range onlineServers {
			if bestServer == nil || server.Priority > bestServer.Priority {
				bestServer = server
			}
		}
		return bestServer
	default:
		return onlineServers[0]
	}
}

// copyData copies data between connections
func (p *MinecraftProxy) copyData(src, dst net.Conn) {
	buffer := make([]byte, 4096)
	for {
		n, err := src.Read(buffer)
		if err != nil {
			if err != io.EOF {
				log.Printf("Read error: %v", err)
			}
			break
		}

		if _, err := dst.Write(buffer[:n]); err != nil {
			log.Printf("Write error: %v", err)
			break
		}

		p.mu.Lock()
		p.stats.BytesTransferred += int64(n)
		p.mu.Unlock()
	}
}

// pingMinecraftServer pings a Minecraft server to check status
func (p *MinecraftProxy) pingMinecraftServer(server *MinecraftServer) string {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", server.Address, server.Port), 5*time.Second)
	if err != nil {
		server.Status = "offline"
		return "offline"
	}
	defer conn.Close()

	// Send Minecraft ping packet
	packet := p.createPingPacket()
	if _, err := conn.Write(packet); err != nil {
		server.Status = "offline"
		return "offline"
	}

	// Read response (simplified)
	buffer := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Read(buffer)
	if err != nil {
		server.Status = "offline"
		return "offline"
	}

	server.Status = "online"
	server.LastPing = time.Now()
	return "online"
}

// createPingPacket creates a Minecraft ping packet
func (p *MinecraftProxy) createPingPacket() []byte {
	var buf bytes.Buffer
	
	// Packet ID (0x00 for handshake)
	buf.WriteByte(0x00)
	
	// Protocol version (varint)
	p.writeVarInt(&buf, 760) // 1.19.2 protocol version
	
	// Server address (string)
	p.writeString(&buf, "localhost")
	
	// Server port (unsigned short)
	binary.Write(&buf, binary.BigEndian, uint16(25565))
	
	// Next state (varint) - 1 for status
	p.writeVarInt(&buf, 1)
	
	// Prepend packet length
	packet := buf.Bytes()
	var finalBuf bytes.Buffer
	p.writeVarInt(&finalBuf, len(packet))
	finalBuf.Write(packet)
	
	return finalBuf.Bytes()
}

// writeVarInt writes a variable-length integer
func (p *MinecraftProxy) writeVarInt(buf *bytes.Buffer, value int) {
	for value >= 0x80 {
		buf.WriteByte(byte(value&0x7F | 0x80))
		value >>= 7
	}
	buf.WriteByte(byte(value & 0x7F))
}

// writeString writes a string with length prefix
func (p *MinecraftProxy) writeString(buf *bytes.Buffer, str string) {
	data := []byte(str)
	p.writeVarInt(buf, len(data))
	buf.Write(data)
}

// startServerPinging starts periodic server health checks
func (p *MinecraftProxy) startServerPinging() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.mu.RLock()
			servers := make([]MinecraftServer, len(p.config.Servers))
			copy(servers, p.config.Servers)
			p.mu.RUnlock()

			for i := range servers {
				p.pingMinecraftServer(&servers[i])
			}

			p.mu.Lock()
			p.config.Servers = servers
			p.mu.Unlock()
		}
	}
}

// sendMaintenanceResponse sends maintenance response to client
func (p *MinecraftProxy) sendMaintenanceResponse(conn net.Conn) {
	// Send maintenance MOTD (simplified implementation)
	maintenance := fmt.Sprintf(`{"version":{"name":"Maintenance","protocol":760},"players":{"max":0,"online":0},"description":{"text":"%s"}}`, p.config.MaintenanceMOTD)
	
	var buf bytes.Buffer
	p.writeString(&buf, maintenance)
	
	packet := buf.Bytes()
	var finalBuf bytes.Buffer
	p.writeVarInt(&finalBuf, len(packet)+1)
	finalBuf.WriteByte(0x00) // Status Response packet ID
	finalBuf.Write(packet)
	
	conn.Write(finalBuf.Bytes())
}

// isBlacklisted checks if an IP is blacklisted
func (p *MinecraftProxy) isBlacklisted(addr string) bool {
	ip := strings.Split(addr, ":")[0]
	
	// Check whitelist first
	if len(p.config.Whitelist) > 0 {
		for _, whiteIP := range p.config.Whitelist {
			if ip == whiteIP {
				return false
			}
		}
		return true // Not in whitelist
	}
	
	// Check blacklist
	for _, blackIP := range p.config.Blacklist {
		if ip == blackIP {
			return true
		}
	}
	
	return false
}

// LoadConfig loads configuration from file
func (p *MinecraftProxy) LoadConfig(filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, p.config)
}

// SaveConfig saves configuration to file
func (p *MinecraftProxy) SaveConfig(filename string) error {
	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}

func main() {
	proxy := NewMinecraftProxy()
	
	// Load configuration if exists
	if err := proxy.LoadConfig("config.json"); err != nil {
		log.Printf("No configuration file found, using defaults")
	}

	proxy.config.Servers = append(proxy.config.Servers, MinecraftServer{
		ID:       "server1",
		Name:     "Main Server",
		Address:  "localhost",
		Port:     25566,
		Status:   "offline",
		Priority: 1,
		Metadata: map[string]string{
			"type": "survival",
		},
	})

	log.Printf("Starting Nautical Minecraft Proxy...")
	if err := proxy.Start(); err != nil {
		log.Fatalf("Failed to start proxy: %v", err)
	}
}
