# 本地人物语音模拟设备

本目录构建当前 Mac 上的统一模拟设备：真实灵矽语音桥、人物页面、Chromium 任务展示及 AirPlay 发送。麦克风和扬声器由 Mac 上的语音网页提供，控制服务、nginx、ngrok 继续使用原有部署。

## 构建与启动

当前 Dockerfile 复用已经验证的本地显示基础镜像 `linxi-device-sim:local`（包含 Chromium、Xvfb、Doubletake 0.5.0 和 GStreamer），不是从空 Docker 环境安装基础镜像的脚本。可通过 `--build-arg DEVICE_BASE=<兼容镜像>` 指定其他基础镜像。

在仓库根目录执行：

```sh
docker build --platform linux/arm64 -f deploy/device-sim/Dockerfile -t linxi-device-sim:avatar .
```

构建上下文使用 Dockerfile 专属白名单，只包括所需 Go 源码、前端资源及入口脚本，不包含 `.env`、`.data/` 或设备身份。构建时运行真实 Opus 和相关 Go race 测试。

使用现有私有设备配置和持久卷启动：

```sh
docker run -d --name linxi-device-sim --init --shm-size=512m \
  --env-file .data/device-sim/device.env \
  -p 127.0.0.1:8787:8787 \
  -p 127.0.0.1:6081:6080 \
  -p 127.0.0.1:8090:8091 \
  -v linxi-device-state:/state \
  linxi-device-sim:avatar
```

已有同名容器时先停止并重命名保留，不要重复创建。复用 `/state` 卷保留灵矽设备绑定和 AirPlay 配对，不复制 AgentAPI 凭据到设备。

配置保留现有 `AIRPLAY_TARGET/AIRPLAY_PORT/AIRPLAY_PASSWORD/AIRPLAY_CREDENTIALS`，并确保 `DISPLAY_ALLOWED_HOSTS` 包含控制服务公开域名（只写主机名，不含协议和路径）。镜像默认设置：

```text
DEVICE_STAGE_URL=http://127.0.0.1:8090/stage/
DEVICE_WAKE_URL=http://127.0.0.1:8788/wake
```

8788 仅供容器内部唤醒，不发布端口，也不添加 ngrok 路由。人物 SSE 和字幕只在本地语音端口提供。健康检查验证 voice HTTP 可访问，不能证明云端语音或远端屏幕当前可用。

## 体验

1. 打开 `http://127.0.0.1:8090/`，刷新页面后连接智能体。
2. 进入 `http://127.0.0.1:8090/stage/` 可直接预览人物；Windows AirPlay 接收端显示的是容器内同一人物状态。
3. 点击“开始说话”，人物进入聆听；点击“说完了，发送”后等待回复。
4. 人物投屏页显示居中的小灵（正面直视用户的蓝紫色小狐狸），通过眨眼、歪头和嘴型交流。人物尺寸约为早期版本的七成；回答时弹出气泡，逐句显示当前实际播放的语音文字，播放结束、打断或断线后收起。较长句子换行显示，并可在气泡内滚动查看。平时不显示品牌、介绍、连接状态或操作按钮。操作与完整对话记录保留在 Mac 语音页面；点击“停止播报”或再次录音会打断。正面狐狸使用用户确认的生成图片，随程序本地提供，来源见 [素材说明](../../internal/voice/ui/stage/fox/SOURCE.md)。
5. 灵矽调用原有 `cast_screen` 时显示任务页，人物缩到右下角。点击“人物全屏 / 唤醒投屏”返回大人物。
6. 嘴型落后声音时，调大“画面与声音同步”里的动作提前量；过早时调小。默认 120 ms。

本版使用按钮唤醒，没有持续监听唤醒词；声音由 Mac 播放，AirPlay 继续只传视频。已知虚拟机时钟导致的 Doubletake 重连行为未在此改动中修复。

日志与检查：

```sh
docker logs -f --tail 80 linxi-device-sim
curl -fsS http://127.0.0.1:8090/api/stage
node --test internal/voice/test/*.test.mjs
make check test build coverage voice-test
```

## 回滚

停止新容器，将其重命名保留；把先前保留的旧容器改回 `linxi-device-sim` 并启动。两个容器不要同时使用同一设备身份在线运行。保持持久卷与旧配置文件不变，刷新 Mac 语音页面并重新连接即可。
