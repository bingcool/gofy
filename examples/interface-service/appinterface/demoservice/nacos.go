package demoservice

// ServiceName is the name demo-service registers with Nacos.
// ServiceName 是 demo-service 注册到 Nacos 的服务名；客户端按它做服务发现，
// 因此改名必须与服务端的注册配置同步。
const ServiceName = "demo-service"
