# 中继恢复测量

在空闲的测量机器上，从待测 checkout 运行：

```sh
python3 scripts/measure-recovery.py --samples 30 --loss-seconds 3600 \
  --label imac --output /tmp/recovery-metrics.json
```

脚本自建并核对 `/tmp` 下的 `REMOTE_AGENT_HOME`，使用 `CGO_ENABLED=0`，只启动本地测试 server、进程级 Link/安全 endpoint 和计数后端；不访问日常账户，不调用原生 CLI，不连接线上，不改网络、路由、防火墙或系统代理。结束后清理账户、数据库和代理，只保留输出 JSON。需要 Go 及本仓库依赖；少于 15 GiB 可用空间即停止。

每个样本记录故障起点、发现、认证 Ready 后一次签名 Link 调用完成，以及尝试数。输出包含全部样本和最近秩 p50/p95/p99，不删除失败或慢样本。RPC 失败不算业务恢复，相应恢复分位不纳入该次失败，但成功数单列，不能把空分位当作通过。

- `rst`：对当前 daemon 主流的本地 TCP socket 设置立即复位并关闭。
- `blackhole`：不转发该旧腿的下行消息，保持客户端连接不结束；新腿正常通信。
- `network_hint`、`same_interface_address`：注入网络观察器的新代次，分别代表电脑换网和同接口地址变化；不修改宿主接口。规范化、重复合并另有单元测试。
- `notification_storm`：连续 1,000 次通知，只保留最新待处理代次；令牌桶与退避照常生效。
- `scheduler_pause`：暂停恢复调度器 3.5 秒，其余进程组件仍运行；从恢复调度时开始计恢复预算。不是整机或整个 daemon 的 OS 挂起测试。
- `bulk_six`、`bulk_eight`：分别占用六个和八个真实共享执行槽，重建 relay 后再发交互调用。八槽全满可能只有 Ready、没有成功调用；如实报告，不能用增加 worker 绕过准入。

丢包观察采用可控的可靠传输替身：下行帧按 1,460 字节单位独立以 3% 概率丢失，200 ms 后重试，最终按原序交付。记录实际传输/丢失单位数、中继发布离线次数、额外拨号数和观察时长。它没有改内核 TCP，不能充当真实 WAN 丢包、移动网络或能耗数据。短跑可指定 `--loss-seconds 120`，但短跑零事件不证明长期每小时上界；正式观察使用至少一小时，并单独保留并发负载、机器休眠和真实手机网络测试。

本测量验证生产 server、Link、签名和结果入口；末端为计数后端。原生 Claude Code/Codex、终端画面和真实按键次数须另做隔离验收。
