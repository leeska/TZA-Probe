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

`bash scripts/pipeline.sh build` 就是这段（外加把发行包与 sha256 一起打出来），
手工做时才需要照着上面两步来。**编完记得把这里的二进制删掉**：它们是被忽略的未跟踪
文件，`git checkout --force` 不会清掉，留着会让之后在同一检出里编出的 Server 内嵌旧 Agent。

要让节点能一键升级，还要在这里放上运维者签名（`tza-agent-linux-<arch>.sig`，由
`tza-server agent-sign` 在你自己的电脑上生成）和公钥 `upgrade-key.pub`；设了
`TZA_AGENT_KEY` 的 `bash scripts/pipeline.sh build` 会自动做。私钥不要放进来。
见 docs/DEPLOYMENT.md「一键升级」。

开发构建可以不放；此时安装脚本会提示“该 Server 未提供此架构的 Agent”。
运行时也可以把同名文件放进数据目录的 `agents/` 子目录，优先于嵌入副本，
更新 Agent 时不必重新编译 Server。
