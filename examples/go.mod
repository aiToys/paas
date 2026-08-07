// 示例项目独立 module：与平台主仓（github.com/aitoys/paas）Go 依赖完全解耦。
// 这两个示例（mcp-server / traffic-gen）是「平台的用户/消费者」，演示如何被平台纳管，
// 不属于 Platform Core，因此不引用 paas 内部包，仅用 Go 标准库。
module github.com/aitoys/paas-examples

go 1.26.0
