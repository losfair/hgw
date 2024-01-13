package netboot

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pin/tftp/v3"
	"go.uber.org/zap"
)

type NetbootConfig struct {
	TftpRoot string `json:"tftp_root"`
}

func (c *NetbootConfig) Start(logger *zap.Logger) {
	h := &tftpHandler{
		logger: logger.With(zap.String("subcomponent", "tftp")),
		root:   c.TftpRoot,
	}
	h.start()
}

type tftpHandler struct {
	logger *zap.Logger
	root   string
}

func (h *tftpHandler) start() {
	s := tftp.NewServer(h.read, nil)
	s.SetTimeout(5 * time.Second)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.logger.Error("tftp server panicked", zap.Any("panic", r))
			}
		}()
		err := s.ListenAndServe(":69")
		if err != nil {
			h.logger.Error("tftp server failed", zap.Error(err))
		}
	}()
	h.logger.Info("tftp server started")
}

func (h *tftpHandler) read(filename string, rf io.ReaderFrom) error {
	if strings.Contains(filename, "..") {
		h.logger.Warn("attempted to read file with .. in path", zap.String("filename", filename))
		return fmt.Errorf("invalid filename")
	}

	file, err := os.Open(path.Join(h.root, filename))
	if err != nil {
		h.logger.Warn("failed to open file", zap.String("filename", filename), zap.Error(err))
		return err
	}
	defer file.Close()

	n, err := rf.ReadFrom(file)
	if err != nil {
		h.logger.Warn("failed to read file", zap.String("filename", filename), zap.Error(err))
		return err
	}

	h.logger.Info("served file", zap.String("filename", filename), zap.Int64("size", n))
	return nil
}
