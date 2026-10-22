---
name: shop-order-query
description: 使用 PaasShop 订单查询工具解释订单状态，缺订单号时先询问，订单不存在或查询失败时如实说明。
license: Apache-2.0
metadata:
  version: 1.0.0
  runtime: instructions
---

# PaasShop 订单查询

向用户取得明确订单号，再调用 `query_order`。参数名与格式遵循 MCP 发现的 schema。根据本次返回的订单号、状态、金额等事实回答；不存在的订单不得补造状态、物流或付款信息。

区分未找到订单、权限不足、服务不可用和真实空结果。查询失败时不能报告退款或发货已完成。

收到退款请求时，只能收集订单和理由并说明需通过退款审批流程；本技能及日常客服没有退款执行权限。不得通过文本、URL 或其他工具绕过审批发起写操作。
