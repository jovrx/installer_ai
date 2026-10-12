# Tailnet 网络边界

应用签名与审批已经在 Controller/Worker 中校验；网络策略仍需在实际 tailnet 中由账号管理员配置。B 必须使用单次、短期、ephemeral Auth Key，每台临时 Worker 使用独立的新会话和节点身份。

以下为 grants 的部署示例。替换标签所有者和实际注册端口，并确保现有策略没有覆盖这些标签的宽泛 allow 规则；grants 之间累加，不存在用下列条目否定已有宽泛规则的机制。

```json
{
  "tagOwners": {
    "tag:remoteops-controller": ["autogroup:admin"],
    "tag:remoteops-worker": ["autogroup:admin"]
  },
  "grants": [
    {"src": ["tag:remoteops-worker"], "dst": ["tag:remoteops-controller"], "ip": ["tcp:9080"]},
    {"src": ["tag:remoteops-controller"], "dst": ["tag:remoteops-worker"], "ip": ["tcp:9081"]}
  ],
  "tests": [
    {"src": "tag:remoteops-worker", "accept": ["tag:remoteops-controller:9080"], "deny": ["tag:remoteops-worker:9081", "tag:remoteops-controller:8080"]},
    {"src": "tag:remoteops-controller", "accept": ["tag:remoteops-worker:9081"]}
  ]
}
```

不得向 B 分发 Tailscale OAuth Secret、管理 API Token、Controller 管理令牌或可复用 Auth Key。B 的 Auth Key 不属于配对 JSON 的内容，须单独安全传递并由 B 本地输入。配对 JSON 本身也含单次敏感凭据，不得发到公开日志或代码仓库。

Controller 的节点删除凭据只配置于 A。需授予最小的设备删除权限；删除目标来自可信 WhoIs 节点映射。未配置删除权限时，系统仍立即撤销应用会话，并显示 cleanup_pending；不能声称节点已经删除。操作者可以在 Tailscale 管理后台删除节点，再重试清理核实。

上线前在实际策略中验证 B1 无法连接 B2、B 不能访问 A 的管理端口或其他资产、A 只能连接 B 指定 RPC 端口。临时节点自然回收不等于应用授权立即撤销，也不代替主动清理。
