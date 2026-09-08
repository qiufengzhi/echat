// Package registry 房间运行时状态存储抽象：在线房间注册表与 AI 状态存储
//
// 定位：把「进程内存单例」收敛为端口接口，调用方（signaling）只依赖接口不感知实现
// 先提供内存实现（行为 = 原 allSignalRooms / global.AIStates 的内存 map），P7 再补 Redis 实现跨进程共享
package registry
