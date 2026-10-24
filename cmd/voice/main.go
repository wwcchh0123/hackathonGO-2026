package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/linx"
	"github.com/wwcchh0123/hackathonGO-2026/internal/voice"
	"github.com/wwcchh0123/hackathonGO-2026/internal/voicecodec"
)

func main() {
	address := flag.String("listen", "127.0.0.1:8090", "loopback HTTP listen address")
	dir := flag.String("data-dir", ".data/voice", "persistent device identity directory")
	pairOnly := flag.Bool("pair-only", false, "activate device without audio or a browser")
	flag.Parse()
	if err := voice.ValidateListen(*address); err != nil {
		log.Fatal(err)
	}
	id, err := linx.LoadIdentity(*dir)
	if err != nil {
		log.Fatal(err)
	}
	path, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	gateway := linx.Gateway{Identity: id, BinaryHash: hex.EncodeToString(hash.Sum(nil))}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s := &voice.Server{Gateway: gateway, AudioAvailable: voicecodec.Available(), Logf: log.Printf}
	log.Printf("Mac 软件设备 ID：%s；身份保存在 %s/identity.json", id.DeviceID, *dir)
	if *pairOnly {
		if err = s.Pair(ctx); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
		return
	}
	if !voicecodec.Available() {
		log.Fatal("语音编解码未启用。请先 brew install opus pkgconf，再 make voice-build；激活可使用 -pair-only")
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		for ctx.Err() == nil {
			if err := s.Pair(ctx); err == nil {
				return
			} else if ctx.Err() == nil {
				log.Printf("连接网关失败：%v；5 秒后重试", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("请用 Chrome 打开 http://%s，连接后点击「开始说话」授权麦克风", listener.Addr())
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
