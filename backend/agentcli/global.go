package agentcli

// Global 全局客户端实例，Init 之后可用
var Global *Client

// Init 创建全局客户端
//
// opts: 地址、分片数、单轮超时与响应回调
// return: 建连参数非法时返回错误
func Init(opts Options) error {
	c, err := New(opts)
	if err != nil {
		return err
	}
	if Global != nil {
		_ = Global.Close()
	}
	Global = c
	return nil
}

// Close 关闭全局客户端
//
// return: 关闭错误，未初始化或全部成功时返回 nil
func Close() error {
	if Global == nil {
		return nil
	}
	err := Global.Close()
	Global = nil
	return err
}
