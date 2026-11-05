# 路由采集定制版：修改与测试结果

2026-11-05，在 40（`deploy`）完成开发与隔离测试。结论：本地验证通过，候选镜像已准备；**没有连接、修改或部署搬瓦工生产服务，正式部署仍等待用户确认。**

## 版本与交付物

- Fork：[bilbilmyc/sub2api](https://github.com/bilbilmyc/sub2api)，上游为 `Wei-Shaw/sub2api`。
- 官方基线：`v0.2.19`，提交 `dece26153621959ccd7635814f4b2d08304e681a`。
- 开发分支：`codex/outbound-evidence-v0.2.19`；审阅基线分支：`codex/base-v0.2.19`。
- 实现提交：`51b6f53efb1b5847a2182a6adc9210adaf1453d5`。后续测试报告提交只修改文档。
- 仓库目录：`/root/codes/sub2api`。
- 候选镜像：`sub2api-route-test:0.2.19-r1`，应用版本 `0.2.19-route.1`，`linux/amd64`，`BuildType=custom`。
- 候选镜像 ID：`sha256:d20e0d6c5aef57283f75c8e18612818f6d5637ab52b93b6ccad6fc25c58482bc`。镜像标签中的 revision 与实现提交一致。
- 本地镜像包：`/root/codes/sub2api/artifacts/sub2api-0.2.19-route.1-amd64.tar`，约 135 MiB；未上传镜像仓库或复制到生产机。
- 镜像包 SHA-256：`4a4c56bfd7d7e5a124b782c1fa7d1b57f946ef187da42915529cda49a8dfdc5a`。

## 修改内容

| 修改 | 最终行为 |
| --- | --- |
| HTTP 出站观察器 | 在实际 HTTP RoundTrip 返回时采集账号、脱敏目标 URL/域名、有效 Host、显式代理、HTTP 状态、上游 `x-request-id` 和等待响应头耗时 |
| 每次尝试独立关联 | 保留入口 request/client ID，每次尝试产生独立 attempt ID；失败、重定向、重试及切号分别记录 |
| Ops 存储 | 通过既有非阻塞队列写入 `route.outbound / outbound.http_attempt` 事件；不增加数据表或迁移 |
| 隐私与转发行为 | 不采集正文、Authorization、Cookie、代理密码或原始错误文本；去除 URL 用户信息、查询参数及片段；不修改请求与响应 |
| 可控启用 | 默认关闭；支持限定账号 ID；关闭或不在范围时直接使用原 HTTP client；启动配置变更需要重启应用 |
| 定制更新保护 | 定制镜像禁用官方在线升级和在线/本地二进制回滚，返回 `CUSTOM_BUILD_MANAGED`；以后通过已验证镜像更新，避免覆盖二开 |
| 可复现测试 | 新增单元测试、内部 Docker 网络、模拟上游和完整 API 测试器；不导入真实账号或生产配置 |

字段、查询方式及启动配置见 [OUTBOUND_EVIDENCE.md](OUTBOUND_EVIDENCE.md)。

## 验证结果

1. `config`、`repository`、`service` 三个受影响包的完整 unit 测试通过。最终代理规范化调整后，重新运行了 repository 的完整 unit 测试。
2. 52 个定向测试及子用例通过 race 检查，包含已有 Ops 回归；另有 OAuth 转发关联测试及其两种模式，共 3 个测试/子用例，通过 race 检查。合计 55 个通过，无失败、无跳过。
3. `go vet -tags=unit` 对上述三个包通过。
4. 候选 Docker 镜像构建成功；前端构建中的 Vue/TypeScript 检查、国际化测试（3/3）通过。最终镜像复用未改动的前端构建缓存。
5. 最终镜像的完整 API 验证于北京时间 15:06:45–15:07:26 完成，以下 9 个场景全部通过。

| 完整链路场景 | 结果 |
| --- | --- |
| 默认关闭 | 正常响应，出站事件为 0 |
| 开启后的非流式请求 | 记录 1 次出站；目标为模拟上游实际 URL；上游 ID 与用量记录一致 |
| 开启后的流式请求 | 同样记录出站并关联用量；流式内容正确返回 |
| 上游不返回 Request ID | 真实目标仍被记录，ID 保持为空，不生成替代值 |
| 503 后自动重试、切号 | 记录 2 次尝试：测试账号 11 返回 503，切到账号 12 后返回 200；最终用量属于账号 12 |
| 限定账号范围 | 范围之外的账号正常请求，出站事件为 0 |
| 定制更新保护 | 官方更新与回滚接口返回 HTTP 409，不执行程序替换 |
| 回滚到未修改的 0.2.19 | 使用同一数据库、同一测试密钥继续请求；本轮 6 条既有用量记录完整保留 |
| 再切回候选镜像 | 应用版本为 `0.2.19-route.1`，同一测试密钥继续请求，采集关闭 |

其余定向覆盖包括 URL/代理凭据脱敏、代理协议规范化、重定向每一跳、共享连接池下 32 个账号并发、流式首块与取消、等待响应头超时、HTTP/2、实际本地 HTTP 代理、缺少关联 ID 的后台探测、长 ID 截断、日志级别/采样隔离、数据库阻塞及队列溢出。

OAuth 路径使用合成 token 和内存上游，验证普通/透传两种 HTTP 模式都会将账号 ID、入口关联 ID、`chatgpt.com` 目标和上游 Request ID 传递到正确边界。**未使用真实 OAuth 账号调用官方服务**；完整 Docker 链路采用模拟 API Key 上游。

未修改基线镜像为 `sub2api-route-baseline:0.2.19`，ID：`sha256:ad8c726dff58c7788a92aaef218eda3ab8632225d6d4706029f56a71b2ae8eff`，从官方 v0.2.19 源码独立构建。它用于兼容性演练，不替代生产现场备份。

## 隔离环境与记录

Compose 项目为 `sub2-route-lab`，使用独立的 PostgreSQL 18、Redis 8、应用数据卷和模拟上游。网络 `internal=true`，没有发布宿主端口，没有生产挂载。测试凭据、管理员门控状态和测试余额均为合成 fixture。

最终检查：应用 healthy，运行上述最终镜像，`GATEWAY_OUTBOUND_EVIDENCE_ENABLED=false`。数据和镜像保留在 40，便于复查。

本地原始记录保存在忽略提交的 `artifacts/`：

- `unit-tests.log`、`repository-unit-final.log`：完整 unit 测试结果。
- `targeted-tests.jsonl`、`oauth-correlation-tests.jsonl`：定向 race 结果。
- `go-vet-final.log`：vet 结果，成功时无输出。
- `image-build.log`、`image-build-final.log`、`baseline-image-build.log`：镜像构建记录。
- `route-lab-final.log`、`route-lab-results.json`：完整 API 验证结果。
- `runtime-verification.json`：最终镜像、健康状态、隔离网络与关闭状态。
- `sub2api-0.2.19-route.1-amd64.tar.sha256`：本地镜像包校验值。

## 结论边界与后续部署

采集证明的是 Sub2 的**直接出站目标及收到的响应头**。它不能追踪服务商内部转发，不能证明官方最终模型、能力或是否“降智”；没有上游 ID 时也不能补造证据。HTTP 200 不代表后续流一定完成。WebSocket、插件绕过 HTTPUpstream 的调用不在本次覆盖范围。

记录沿用 Ops 保留期；队列满或数据库异常时可能丢弃观测数据。测试验证了转发不等待慢数据库，没有做生产负载/长期存储基准测试，单次请求计时不能作为性能结论。

本次没有调整生产镜像、Compose、数据库、代理或账号设置。用户确认部署后，仍需先备份生产**实际运行的程序/容器和数据**，再安排应用重建及限定账号验证。生产使用过在线更新，因此不能仅凭旧镜像 tag 认定能够回滚。此次没有执行这些生产操作，也没有承诺无停机切换。
