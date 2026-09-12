package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

const maxPairingBundleBytes = 64 * 1024

type pairingServer struct {
	server   *http.Server
	listener net.Listener
	url      string
	acked    chan struct{}
	serveErr chan error
}

func startPairingServer(host string, port int, bundle []byte) (*pairingServer, error) {
	if len(bundle) == 0 || len(bundle) > maxPairingBundleBytes {
		return nil, fmt.Errorf("配对数据大小必须是 1..%d 字节", maxPairingBundleBytes)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() {
		return nil, errors.New("临时配对服务要求 --host 是手机可访问的本机 IPv4 地址")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("启动临时配对服务 %s: %w", host, err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("生成配对令牌: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	path := "/p/" + token
	portNumber := listener.Addr().(*net.TCPAddr).Port
	endpoint := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(portNumber)), Path: path}).String()

	acked := make(chan struct{}, 1)
	var downloaded atomic.Bool
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if request.URL.Path != path || request.URL.RawQuery != "" {
			http.NotFound(writer, request)
			return
		}
		switch request.Method {
		case http.MethodGet:
			if !downloaded.CompareAndSwap(false, true) {
				http.Error(writer, "pairing bundle already downloaded", http.StatusGone)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Content-Length", strconv.Itoa(len(bundle)))
			writer.WriteHeader(http.StatusOK)
			if _, err := writer.Write(bundle); err != nil {
				downloaded.Store(false)
			}
		case http.MethodPost:
			if !downloaded.Load() {
				http.Error(writer, "bundle has not been downloaded", http.StatusConflict)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			select {
			case acked <- struct{}{}:
			default:
			}
		default:
			writer.Header().Set("Allow", "GET, POST")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	instance := &pairingServer{
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       10 * time.Second,
			MaxHeaderBytes:    8 * 1024,
		},
		listener: listener,
		url:      endpoint,
		acked:    acked,
		serveErr: make(chan error, 1),
	}
	go func() {
		if err := instance.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			instance.serveErr <- err
		}
	}()
	return instance, nil
}

func (server *pairingServer) URL() string { return server.url }

func (server *pairingServer) Wait(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-server.acked:
		return nil
	case err := <-server.serveErr:
		return fmt.Errorf("临时配对服务异常: %w", err)
	case <-timer.C:
		return errors.New("配对二维码已过期，请重新运行 pair")
	}
}

func (server *pairingServer) Close() {
	_ = server.server.Close()
}
