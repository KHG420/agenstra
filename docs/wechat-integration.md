# 微信通道插件

Go SDK 的 `NewWeChatPlugin` 提供微信 iLink 通道接入。扫码登录、消息轮询、输入状态、文本分段、文件加密上传和原生文件投递复用同一插件。实现位于 `internal/platform/wechat`，公开类型和构造函数由 `sdk/go` 导出。

插件使用当前绑定的 bot、peer 和凭据。宿主把该绑定解析为可信 `owner`，通过现有 `AgentHost`、聊天会话和实时策略执行请求，并持久保存接收游标、去重标识和投递回执。绑定与文件密钥应使用宿主的加密存储。

## 登录与消息接收

```go
plugin := agenstra.NewWeChatPlugin(nil)
defer plugin.Close()

pending, err := plugin.QRCode(ctx)
if err != nil {
    return err
}
svg, err := agenstra.WeChatQRCodeSVG(pending.QRContent)
if err != nil {
    return err
}
// Render svg in the authenticated owner's binding view.
_ = svg

login, err := plugin.LoginStatus(ctx, pending, verificationCode)
if err != nil {
    return err
}
// On confirmed, persist login.Token, login.BotID, login.PeerID and the
// validated login.BaseURL under this authenticated owner.
```

登录状态沿用平台协议。宿主在每次绑定操作中核验当前账号，将确认后的凭据保存到对应账号。平台返回重定向主机时，使用 `ValidateWeChatBaseURL` 验证其 HTTPS origin。验证码与扫码确认由登录者完成。

调用 `PollUpdates` 时传入最后提交的 `Credentials.Cursor`。对每条消息先执行 `credentials.Accepts(message)`，再通过 `WeChatMessageKey` 取得 bot 与 peer 范围内的稳定标识。在同一宿主事务中保存入站消息与下一游标，重复标识复用原聊天消息和任务。`WeChatID` 保留平台字符串或数值 ID 的精度。

宿主按绑定读取当前 `owner` 的策略和能力授权，将输入提交到该账号的现有聊天会话。手机号、微信 peer 和模型文本都通过可信绑定解析为业务账号。会话、记忆、用量和后台任务继续调用框架原有接口。

## 文本与文件回复

`WeChatTextParts` 接受最多 64 KiB 的 UTF-8 文本，返回每段最多 2,000 字节的切片。宿主为每段保存稳定 `client_id` 和投递位置，在成功回执后推进游标。最终核验通过的答复可以按这些段依次投递。

```go
file, err := agenstra.NewWeChatFile(fileName, fileBytes)
if err != nil {
    return err
}
// Persist this upload identity in the owner's encrypted outbox.
uploaded, err := plugin.UploadFile(ctx, credentials, file)
if err != nil {
    return err
}
// Persist uploaded before sending so recovery reuses the same CDN reference.
err = plugin.SendFile(ctx, credentials, clientID, contextToken, uploaded)
if err != nil {
    return err
}
```

文件上限为 8 MiB。`NewWeChatFile` 复制调用者的数据并生成上传身份和 AES 密钥。`UploadFile` 使用 AES-128-ECB 与 PKCS#7 加密，将文件以 `media_type=3` 上传到微信 CDN，并返回不含原始数据的文件引用。`SendFile` 发送 `type=4` 的原生附件，保留文件名与原始字节数。PDF、图片或其他文件内容由宿主现有业务接口取得。

`SendText` 与 `SendFile` 每次执行一次发送。`WeChatSendError` 表示平台明确拒绝，保留 `Ret` 与 `Code`。宿主按持久投递身份实施有界重试。网络中断、超时或进程退出后的发送结果需要保留为不确定状态，并核对实际回执。

## 请求与资源生命周期

每个请求接受调用者的 `context.Context`，最长 42 秒，平台 JSON 响应最多 2 MiB。默认 HTTP transport 限制连接数量。文件 CDN 地址与 API 主机在发送前校验，bot 凭据只进入平台 API 请求。插件阻止 HTTP 重定向，并在每次请求中使用独立 client 配置。

宿主传入的 HTTP client 保持调用者所有。插件使用其 transport，保留原 client、CookieJar 和 timeout 配置。宿主应先停止自己的轮询和投递 worker，再调用 `Close` 释放插件自行创建的空闲连接。

协议依据腾讯公开的 iLink 客户端协议与原生文件消息实现。[腾讯协议参考](https://github.com/Tencent/openclaw-weixin/blob/main/docs/protocol.md)
