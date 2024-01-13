package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/bogosj/tesla"
	"github.com/gin-gonic/gin"
	"github.com/losfair/hgw/homegw-libs/disk"
	"github.com/losfair/hgw/homegw-libs/kexec"
	rt_control "github.com/losfair/hgw/homegw-libs/rt-control"
	tesla_api "github.com/losfair/hgw/homegw-libs/tesla-api"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

type ApiServerConfig struct {
	Listen                       string              `json:"listen"`
	Certificates                 []CertificateConfig `json:"certificates"`
	ClientKeys                   []ClientKey         `json:"client_keys"`
	MaxConcurrentQuicConnections int                 `json:"max_concurrent_quic_connections"`
	StatelessResetKey            string              `json:"stateless_reset_key"`
	ExtResetAllowedPins          []string            `json:"ext_reset_allowed_pins"`
	AllowCrash                   bool                `json:"allow_crash"`
}

type CertificateConfig struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type ClientKey struct {
	Id     string   `json:"id"`
	Secret string   `json:"secret"`
	Scopes []string `json:"scopes"`
}

type ApiServer struct {
	Logger                 *zap.Logger
	Version                int64
	Config                 *ApiServerConfig
	TeslaApi               []*tesla_api.VehicleApi
	KexecEnabled           bool
	KexecEncryptionKey     [32]byte
	Disks                  []disk.DiskConfig
	KexecSshKill           chan<- struct{} // protected by globalKexecLock
	KexecSshKillCompletion <-chan struct{} // protected by globalKexecLock
}

var rtControlClient = http.Client{
	Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", rt_control.SocketPath)
		},
	},
}

func (s *ApiServer) Run() error {
	g := gin.New()
	g.Use(gin.Recovery())

	// max concurrency = 10
	// reject requests above this
	sem := make(chan struct{}, 10)
	g.Use(func(ctx *gin.Context) {
		// kexec is not subject to rate limiting
		if strings.HasPrefix(ctx.Request.URL.Path, "/kexec/") {
			ctx.Next()
			return
		}

		select {
		case sem <- struct{}{}:
			defer func() {
				<-sem
			}()
			ctx.Next()
		default:
			ctx.JSON(429, gin.H{"error": "too many requests"})
		}
	})

	{
		accounts := gin.Accounts{}
		for _, clientKey := range s.Config.ClientKeys {
			if lo.Contains(clientKey.Scopes, "tesla") && clientKey.Secret != "" {
				accounts[clientKey.Id] = clientKey.Secret
			}
		}
		if len(accounts) != 0 {
			group := g.Group("/tesla", gin.BasicAuth(accounts))
			group.GET("/:vin/vehicle_data", s.vehicleData)
			s.Logger.Info("enabled api", zap.String("api", "tesla"))
		}
	}

	{
		accounts := gin.Accounts{}
		for _, clientKey := range s.Config.ClientKeys {
			if lo.Contains(clientKey.Scopes, "kexec") && clientKey.Secret != "" {
				accounts[clientKey.Id] = clientKey.Secret
			}
		}
		if len(accounts) != 0 {
			group := g.Group("/kexec", gin.BasicAuth(accounts))
			group.POST("/trigger", s.kexec)
			s.Logger.Info("enabled api", zap.String("api", "kexec"))
		}
	}

	{
		accounts := gin.Accounts{}
		for _, clientKey := range s.Config.ClientKeys {
			if lo.Contains(clientKey.Scopes, "debug") && clientKey.Secret != "" {
				accounts[clientKey.Id] = clientKey.Secret
			}
		}
		if len(accounts) != 0 {
			group := g.Group("/debug", gin.BasicAuth(accounts))
			group.POST("/kill_dropbear", s.debugKillDropbear)
			group.GET("/public_ip", s.debugGetPublicIp)
			group.POST("/ext_reset/:pin", s.debugExtReset)

			group.POST("/panic", func(ctx *gin.Context) {
				panic("test panic")
			})
			group.POST("/crash", func(ctx *gin.Context) {
				if !s.Config.AllowCrash {
					ctx.JSON(403, gin.H{"error": "crash not allowed"})
					return
				}

				log.Println("requested to crash, calling os.Exit(1)")
				os.Exit(1)
			})
			s.Logger.Info("enabled api", zap.String("api", "debug"))
		}
	}

	tlsCerts := make([]tls.Certificate, len(s.Config.Certificates))

	for i, cert := range s.Config.Certificates {
		c, err := tls.X509KeyPair([]byte(cert.Cert), []byte(cert.Key))
		if err != nil {
			return err
		}
		tlsCerts[i] = c
	}

	s.startQuicServer(tlsCerts, g)

	h2s := &http.Server{Addr: s.Config.Listen, Handler: g.Handler(), TLSConfig: &tls.Config{Certificates: tlsCerts}}
	s.Logger.Info("starting h2 api server")
	return h2s.ListenAndServeTLS("", "")
}

func (s *ApiServer) startQuicServer(certs []tls.Certificate, g *gin.Engine) {
	logger := s.Logger.With(zap.String("protocol", "quic"))
	udpServer, err := net.ListenPacket("udp", s.Config.Listen)
	if err != nil {
		logger.Error("udp server listen failed", zap.Error(err))
		return
	}
	transport := quic.Transport{Conn: udpServer}

	if key, err := base64.StdEncoding.DecodeString(s.Config.StatelessResetKey); err == nil && len(key) == 32 {
		var buf quic.StatelessResetKey
		copy(buf[:32], key[:32])
		transport.StatelessResetKey = &buf
		logger.Info("loaded stateless reset key")
	}

	quicServer, err := transport.Listen(&tls.Config{
		Certificates: certs,
		NextProtos:   []string{"h3", "quicssh"},
	}, nil)
	if err != nil {
		logger.Error("quic server listen failed", zap.Error(err))
		return
	}

	h3s := &http3.Server{Handler: g.Handler()}
	maxConcurrency := s.Config.MaxConcurrentQuicConnections
	if maxConcurrency == 0 {
		maxConcurrency = 100
	}
	sem := make(chan struct{}, maxConcurrency)
	logger.Info("starting quic server", zap.Int("max_concurrent_connections", maxConcurrency), zap.String("listen", s.Config.Listen))

	go func() {
		for {
			sem <- struct{}{}
			conn, err := quicServer.Accept(context.Background())
			if err != nil {
				logger.Error("quic accept failed", zap.Error(err))
				return
			}

			negotiatedProtocol := conn.ConnectionState().TLS.NegotiatedProtocol
			logger := logger.With(zap.String("peer", conn.RemoteAddr().String()), zap.String("negotiated_protocol", negotiatedProtocol))

			go func() {
				defer func() { <-sem }()
				defer conn.CloseWithError(0, "close")
				if negotiatedProtocol == "quicssh" {
					relayToLocalSSH(logger, conn)
				} else {
					if err := h3s.ServeQUICConn(conn); err != nil {
						logger.Debug("http3 conn error", zap.Error(err))
					}
				}
			}()
		}
	}()
}

func (s *ApiServer) vehicleData(c *gin.Context) {
	vin := c.Param("vin")
	v, ok := lo.Find(s.TeslaApi, func(v *tesla_api.VehicleApi) bool { return v.Vin == vin })
	if !ok {
		c.JSON(404, gin.H{"error": "vehicle not found"})
		return
	}

	var vehicle *tesla.Vehicle
	select {
	case <-v.Vehicle().Ready():
		vehicle = v.Vehicle().WaitForValue()
	case <-c.Request.Context().Done():
		return
	}

	select {
	case v.Sem <- struct{}{}:
	default:
		c.JSON(429, gin.H{"error": "too many requests"})
		return
	}
	defer func() { <-v.Sem }()

	data, err := vehicle.Data()
	if err != nil {
		s.Logger.Warn("failed to fetch vehicle data", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to fetch vehicle data"})
		return
	}

	c.JSON(200, gin.H{"data": data})
}

func (s *ApiServer) debugKillDropbear(c *gin.Context) {
	exec.Command("killall", "-9", "dropbear").Run()
	c.JSON(200, gin.H{"status": "ok"})
}

func (s *ApiServer) debugGetPublicIp(c *gin.Context) {
	logger := s.Logger.With(zap.String("api", "debugGetPublicIp"))
	req, err := http.NewRequestWithContext(c.Request.Context(), "GET", "https://api.ipify.org/", nil)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to create request"})
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Warn("failed to perform request to ip api", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to perform request"})
		return
	}

	if resp.StatusCode != 200 {
		logger.Warn("failed to get public ip", zap.Int("status", resp.StatusCode))
		c.JSON(500, gin.H{"error": "request failed"})
		return
	}

	defer resp.Body.Close()
	ip, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		logger.Warn("failed to read response body", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to read response body"})
		return
	}

	c.JSON(200, gin.H{"ip": string(ip)})
}

func (s *ApiServer) debugExtReset(c *gin.Context) {
	pin := c.Param("pin")
	if !lo.Contains(s.Config.ExtResetAllowedPins, pin) {
		c.JSON(403, gin.H{"error": "pin not allowed"})
		return
	}

	response, err := rtControlClient.Post(fmt.Sprintf("http://unix/ext_reset/%s", pin), "text/plain", nil)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to perform request"})
		return
	}

	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to read response body"})
		return
	}

	if response.StatusCode != 200 {
		c.JSON(500, gin.H{"success": false, "result": string(text)})
	} else {
		c.JSON(200, gin.H{"success": true, "result": string(text)})
	}
}

var globalKexecLock sync.Mutex

func (s *ApiServer) kexec(c *gin.Context) {
	if !s.KexecEnabled {
		c.JSON(400, gin.H{"error": "kexec disabled"})
		return
	}

	if !globalKexecLock.TryLock() {
		c.JSON(409, gin.H{"error": "kexec already in progress"})
		return
	}
	defer globalKexecLock.Unlock()

	if c.Request.ContentLength > 128*1048576 {
		c.JSON(400, gin.H{"error": "kexec package is too big"})
		return
	}

	input := make([]byte, c.Request.ContentLength)
	_, err := io.ReadFull(c.Request.Body, input)
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read request body"})
		return
	}

	pkg, err := kexec.Unseal(s.Logger, s.Version, s.KexecEncryptionKey, input, c.Request.Header.Get("X-External-Signature"))
	if err != nil {
		s.Logger.Error("failed to unseal kexec package", zap.Error(err))
		c.JSON(400, gin.H{"error": "failed to unseal kexec package"})
		return
	}

	// Free up memory as much as possible before doing kexec_load
	// Kill SSH
	if s.KexecSshKill != nil {
		close(s.KexecSshKill)
		s.KexecSshKill = nil
		<-s.KexecSshKillCompletion
	}
	// Kill user processes
	// Two continuous pkills to work around race condition
	for {
		cmd := exec.Command("pkill", "-SIGKILL", "-U", "1000")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if cmd.Run() != nil {
			cmd = exec.Command("pkill", "-SIGKILL", "-U", "1000")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if cmd.Run() != nil {
				s.Logger.Info("killed all user processes")
				break
			}
		}
	}
	// Unmount user tmp
	err = syscall.Unmount("/vroot/tmp", 0)
	if err != nil {
		s.Logger.Error("failed to unmount /vroot/tmp", zap.Error(err))
	}
	// Unmount disks
	for _, disk := range s.Disks {
		logger := s.Logger.With(zap.String("device", disk.Device), zap.String("encrypted_device", disk.EncryptedDevice), zap.String("mountpoint", disk.Mountpoint))
		err := syscall.Unmount(disk.Mountpoint, 0)
		if err != nil {
			logger.Error("failed to unmount disk", zap.Error(err))
		} else {
			logger.Info("unmounted disk")
		}
	}

	// Save config for the new kernel
	if err := writeConfigToOcram(pkg.Config); err != nil {
		s.Logger.Error("failed to write config to ocram", zap.Error(err))
		c.JSON(400, gin.H{"error": "failed to write config to ocram"})
		return
	}

	kexecSegment := make([]uintptr, 4)
	kexecSegment[0] = uintptr(unsafe.Pointer(&pkg.Kernel[0]))
	kexecSegment[1] = uintptr(len(pkg.Kernel))
	kexecSegment[2] = 0x80000000
	kexecSegment[3] = 0x8000000

	_, _, errno := syscall.Syscall6(syscall.SYS_KEXEC_LOAD, 0x80000000, 1, uintptr(unsafe.Pointer(&kexecSegment[0])), 0, 0, 0)
	runtime.KeepAlive(kexecSegment)
	runtime.KeepAlive(pkg)

	if errno != 0 {
		s.Logger.Error("failed to load kexec package", zap.Error(errno))
		c.JSON(400, gin.H{"error": "failed to load kexec package"})
		return
	}

	s.Logger.Info("kexec image loaded, rebooting")

	// Blocking network requests should not block reboot
	{
		syncCompletion := make(chan struct{})
		go func() {
			s.Logger.Sync()
			c.Writer.WriteHeader(200)
			c.Writer.WriteString("Rebooting into new kernel\n")
			c.Writer.Flush()
			close(syncCompletion)
		}()

		select {
		case <-syncCompletion:
		case <-time.After(15 * time.Second):
		}
	}

	_, _, errno = syscall.Syscall6(syscall.SYS_REBOOT, syscall.LINUX_REBOOT_MAGIC1, syscall.LINUX_REBOOT_MAGIC2, syscall.LINUX_REBOOT_CMD_KEXEC, 0, 0, 0)
	if errno != 0 {
		s.Logger.Fatal("failed to reboot", zap.Error(errno))
	}
}
