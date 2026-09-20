# 嵌入的 Agent 二进制

发布构建前把交叉编译好的 Agent 放到这里，Server 会把它们嵌入并通过
`/dl/tza-agent-linux-<arch>` 分发给一键安装脚本：

```sh
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags='-s -w' \
    -o internal/agentdist/bin/tza-agent-linux-$arch ./cmd/agent
done
go build -trimpath -ldflags='-s -w' -o bin/tza-server ./cmd/server
```

开发构建可以不放；此时安装脚本会提示“该 Server 未提供此架构的 Agent”。
运行时也可以把同名文件放进数据目录的 `agents/` 子目录，优先于嵌入副本，
更新 Agent 时不必重新编译 Server。
