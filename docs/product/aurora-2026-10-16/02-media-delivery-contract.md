# [P0] 对齐媒体工具的名字、上传和交付契约

Status: open follow-up; not implemented in this UI change.
Published issue: https://github.com/apexai-sg/multica/issues/2

## 证据（基线 main 14be4ef）

`server/internal/aurora/experts_catalog.go` 的24个专家统一接受文字和可选文档，登记输出为文字；`catalog.go` 将其全部标记为 available。
`experts/media-video-editing/workflow.md` 包含视频剪辑步骤，但最终 Required outputs 和 manifest 只允许 `media-video-editing.md`。

## 验收清单

- [ ] 每项功能明确输入文件、凭据、实际工具、可交付文件及环境。
- [ ] 文本路线只能宣传脚本或方案，不能宣传成片或图片。
- [ ] 图片、视频、PPT、Excel等功能完成真实上传、执行、产物写回、预览/下载和结算后才启用。
- [ ] 可用性依据环境前提检查，而非仅静态 available 标记。
- [ ] 按具体任务选择子技能，不强制执行所有步骤。
- [ ] 缺资料、执行失败和无产物的费用语义可核对。
- [ ] 在明确授权的测试环境跑真实模型/支付测试，记录实际文件和扣费证据。

本次前端改版仅纠正展示和标记规划入口，不代表这些链路已接通。
