# 本地 Web 接入示例

在仓库根目录执行 `go run ./examples/web-integration`，打开 `http://127.0.0.1:8092`。

发送“查询待处理订单并显示列表”或“打开订单 1001”。聊天框、SDK、消息排队和审批由框架提供；host.js 只实现宿主页面和两个业务动作。也可连续发送多条消息观察排队，或发送“先让我补充筛选条件”体验追问。

示例使用演示数据和固定 DecisionModel，无需模型密钥。orders.list 通过 REST Provider 调用本地 /demo/orders，浏览器动作等待实际 handler 回执。默认只监听回环地址，`--addr 127.0.0.1:8093` 可更换端口。退出时删除临时数据库。

演示身份固定为 demo；实际接入必须验证宿主登录身份并保存运行库和扩展库。完整方式见 [Web 接入指南](../../docs/web-integration.md)。
