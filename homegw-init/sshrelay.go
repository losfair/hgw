package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"go.uber.org/zap"
)

func relayToLocalSSH(logger *zap.Logger, conn quic.Connection) {
	sem := make(chan struct{}, 1)

	for {
		select {
		case sem <- struct{}{}:
		case <-conn.Context().Done():
			logger.Info("closing relay")
			return
		}

		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			logger.Error("failed to accept stream", zap.Error(err))
			return
		}

		go func() {
			defer func() { <-sem }()
			defer stream.Close()

			logger := logger.With(zap.Int64("stream_id", int64(stream.StreamID())))

			// relay to tcp 127.0.0.1:22
			dialer := net.Dialer{}
			sshConn, err := dialer.DialContext(conn.Context(), "tcp", "127.0.0.1:22")
			if err != nil {
				logger.Error("failed to dial ssh", zap.Error(err))
				return
			}
			defer sshConn.Close()

			logger.Info("established ssh connection")

			var wg sync.WaitGroup

			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := io.Copy(stream, sshConn); err != nil && err != io.EOF && !errors.Is(err, net.ErrClosed) {
					logger.Error("failed to copy from ssh to quic", zap.Error(err))
				}
			}()
			go func() {
				defer wg.Done()
				if _, err := io.Copy(sshConn, stream); err != nil && err != io.EOF && !errors.Is(err, net.ErrClosed) {
					logger.Error("failed to copy from quic to ssh", zap.Error(err))
				}
				sshConn.SetReadDeadline(time.Now())
			}()
			wg.Wait()
		}()
	}

}
