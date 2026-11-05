# HTTP 出站路由采集（定制分支）

基线为官方 `v0.2.19`，提交 `dece26153621959ccd7635814f4b2d08304e681a`。本分支只在开发机的隔离环境验证，尚未部署生产。测试记录见 [ROUTE_TEST_REPORT.md](ROUTE_TEST_REPORT.md)。

## 采集内容

在 HTTPUpstream 的实际 `RoundTrip` 返回时记录账号、脱敏后的真实请求 URL、有效 Host、显式代理 host:port、HTTP 响应头状态、直接上游 `x-request-id`、等待响应头耗时，以及两种入口关联 ID。每次尝试有独立的 `attempt_id`；重定向各跳、重试及切号后的调用分别保留。

URL 会删除用户信息、查询参数与片段。响应头只读取 `x-request-id`，最多 128 字节，截断显式标记。请求和响应正文、Authorization、Cookie、账号 token、代理密码及原始错误文本不进入事件。该观察器不修改请求、响应头或响应体。

事件只进入现有 Ops 队列，绕过普通控制台/文件日志及其采样设置。队列满时丢弃观测事件并增加已有 `dropped_count`，数据库写入在后台完成。数据库缓慢或不可用时，转发调用不等待数据库写入；日志不是无损审计。

## 启动配置

默认关闭；关闭时使用原来的 HTTP client。启用示例：

```yaml
environment:
  GATEWAY_OUTBOUND_EVIDENCE_ENABLED: "true"
  GATEWAY_OUTBOUND_EVIDENCE_ACCOUNT_IDS: "6,29"
```

账号列表为空表示所有账号；非空时只记录列出的正整数账号 ID。列表中的非正数会阻止配置加载。没有入口关联 ID 的后台探测不会进入路由事件。配置在启动时读取，更改后需要重启应用；它不是热更新开关。

YAML 配置等价项：

```yaml
gateway:
  outbound_evidence_enabled: false
  outbound_evidence_account_ids: []
```

## 接口与关联

沿用管理员 `GET /api/v1/admin/ops/system-logs`，示例查询参数为 `component=route.outbound&client_request_id=...`。时间范围使用现有 `start_time/end_time`；分页使用现有 `page/page_size`。只读取 `component=route.outbound`、`message=outbound.http_attempt`、`extra.schema_version=1` 的事件。

| 字段 | 含义 |
| --- | --- |
| 顶层 `account_id/request_id/client_request_id` | 同时由 Ops sink 写入索引；关联查询需使用正确命名空间和精确 ID |
| `extra.attempt_id` | 每次 HTTP 尝试的独立 UUID |
| `upstream_url/upstream_host` | 此次实际构造的请求目标；URL 已脱敏 |
| `request_host/effective_host` | 显式 Host 及最终有效 Host |
| `proxy_host/proxy_scheme` | 实际传入 HTTP 客户端的显式代理；没有代理凭据 |
| `status_code/response_received/transport_error` | 收到响应头的状态；无响应时状态为 0 |
| `upstream_request_id_header/upstream_request_id` | 固定读取 `x-request-id`，无该响应头时为空 |
| `upstream_request_id_truncated` | 超过长度限制时为 true，不能用作严格最终归属 |
| `headers_elapsed_ms` | 等待响应头的耗时，非整个流的耗时 |

这些字段与 Sub2 Manager 的 schema v1 消费端兼容。同一个请求可能经历多个账号；不能仅按最终账号筛选，否则会丢失前面的失败尝试。只有同账号的非截断上游 ID 与唯一 HTTP 响应记录吻合时，才能将它关联到最终用量；字段缺失时保留未知。

## 能力范围

- HTTP 2xx 只说明收到响应头，流式响应之后仍可能失败。
- 上游不返回 Request ID 时继续留空；不会用本地 ID 或响应对象 ID 冒充。
- 覆盖经过 HTTPUpstream 的 HTTP 调用。WebSocket、插件内部调用以及绕过该入口的网络调用不在覆盖范围。
- 请求校验或获取客户端阶段失败时还未进入 RoundTrip，不生成虚构的 HTTP 尝试。底层 Transport 内部自动重传不会进一步拆分。
- 记录的是 Sub2 直接出站目标。第三方转发商内部的下一跳，以及最终模型能力，无法由这些字段独立证明。
- 沿用 Ops 日志保留期，队列丢弃、数据库故障和清理都可能导致记录不完整。

## 定制构建的更新保护

根目录 Dockerfile 默认使用 `BUILD_TYPE=custom`。该构建的官方在线更新、在线版本回滚和本地二进制回滚接口返回 `CUSTOM_BUILD_MANAGED`，避免下载官方程序覆盖定制功能；检查更新只返回当前版本及说明。普通官方 `release/source` 构建原逻辑保持有效。

定制版本通过验证后的镜像切换。若未来希望保留网页在线更新，需要单独接入自己的 Release、版本比较和回滚策略。本分支没有发布 GitHub Release，也没有启用自动部署工作流；现有上游发布工作流不作为此定制版本的发布入口。

## 本地复现

使用独立的 `sub2-route-lab` Compose 项目和新数据卷。网络是 `internal: true`，不映射宿主端口，不挂载生产配置，也不导入真实账号。测试器从 Docker 获取该项目的私有容器地址；模拟上游不会连接任何外部服务。

在仓库根目录构建：

```sh
mkdir -p artifacts
CGO_ENABLED=0 go build -o artifacts/route-mock deploy/tests/route-mock/main.go
docker buildx build --platform linux/amd64 --build-arg VERSION=0.2.19-route.1 \
  --build-arg COMMIT="$(git rev-parse HEAD)" --load -t sub2api-route-test:0.2.19-r1 .
```

回滚演练还需要从未修改的官方 v0.2.19 源码构建 `sub2api-route-baseline:0.2.19`，显式传入 `VERSION=0.2.19`。Dockerfile 中的前端构建包含类型检查及已有的国际化测试。

```sh
docker compose -p sub2-route-lab -f deploy/compose.route-lab.yaml up -d --wait
python3 deploy/tests/route-lab.py
```

本地测试账号、余额和管理员门控状态都是合成 fixture；未代真实用户提交协议确认。测试器验证默认关闭、启用、限定账号、流式/非流式、上游缺少 ID、自动切号、更新保护，以及换回原版后继续使用同一测试密钥和历史用量。结束时回到候选镜像且采集关闭。结果保存在忽略提交的 `artifacts/route-lab-results.json`。

源码检查：

```sh
cd backend
go test -tags=unit ./internal/config ./internal/repository ./internal/service
go test -race -tags=unit ./internal/config ./internal/repository ./internal/service \
  -run 'TestOutboundEvidence|TestOpsSystemLogSink|TestCustomBuild' -count=1
go vet -tags=unit ./internal/config ./internal/repository ./internal/service
```

正式部署须另行批准：保存真实运行程序的可回滚镜像、验证数据备份，确认维护窗口和启动配置，再只切换应用服务。本次开发与测试没有连接或修改生产服务器。
