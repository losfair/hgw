package rt_control

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const SocketPath = "/run/homegw-rt.sock"

type RtControl struct {
	process *os.Process
}

func NewRtControl(logger *zap.Logger, initConfig []byte) *RtControl {
	pr, pw, err := os.Pipe()
	if err != nil {
		logger.Fatal("failed to create pipe", zap.Error(err))
	}
	defer pw.Close()

	unixListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: SocketPath, Net: "unix"})
	if err != nil {
		logger.Fatal("failed to listen on unix socket", zap.Error(err))
	}

	os.Chmod(SocketPath, 0664)
	os.Chown(SocketPath, 0, 1000)

	// `unixListener` is LEAKED here, to prevent the socket file from being deleted.
	unixListenerFile, err := unixListener.File()
	if err != nil {
		logger.Fatal("failed to get unix listener file", zap.Error(err))
	}
	defer unixListenerFile.Close()

	cmd := exec.Command("/homegw-rt")
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		logger.Fatal("failed to get stdin pipe", zap.Error(err))
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{pw, unixListenerFile}
	err = cmd.Start()
	if err != nil {
		logger.Fatal("failed to start homegw-rt", zap.Error(err))
	}

	go func() {
		stdinPipe.Write(initConfig)
		stdinPipe.Close()
	}()

	go func() {
		defer func() {
			logger.Fatal("homegw-rt exited")
		}()

		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			var msg map[string]interface{}
			level := zapcore.InfoLevel
			message := "homegw-rt log"
			target := ""
			fields := make(map[string]interface{})
			err := json.Unmarshal(scanner.Bytes(), &msg)
			if err != nil {
				logger.Error("failed to parse homegw-rt message", zap.Error(err))
				continue
			}
			if msg != nil {
				if x, ok := msg["fields"].(map[string]interface{}); ok {
					fields = x
					if x, ok := fields["message"].(string); ok {
						message = x
						delete(fields, "message")
					}
				}
				if x, ok := msg["level"].(string); ok {
					switch x {
					case "ERROR":
						level = zapcore.ErrorLevel
					case "WARN":
						level = zapcore.WarnLevel
					case "INFO":
						level = zapcore.InfoLevel
					case "DEBUG":
						level = zapcore.DebugLevel
					case "TRACE":
						level = zapcore.DebugLevel
					default:
						level = zapcore.InfoLevel
					}
				}
				if x, ok := msg["target"].(string); ok {
					target = x
				}
			}
			logger.Log(level, message, zap.String("target", target), zap.Any("fields", fields))
		}
		if err := scanner.Err(); err != nil {
			logger.Error("failed to read from homegw-rt", zap.Error(err))
		}
	}()

	return &RtControl{
		process: cmd.Process,
	}
}
