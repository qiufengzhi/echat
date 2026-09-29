"""协程 await 与不 await 的行为对比演示脚本

说明：直接运行即可观察「await 阻塞等待」与「只创建协程对象不 await」
两种写法的耗时差异，用于理解 asyncio 的基本执行模型
"""

import asyncio
import time


async def work(name: str, delay: float) -> str:
    """模拟一个耗时任务：打印开始结束时间后休眠

    name: 任务名，仅用于日志显示
    delay: 休眠秒数，模拟任务耗时
    return: 任务名
    """
    print(f"{name} start at {time.strftime('%H:%M:%S')}")
    await asyncio.sleep(delay)
    print(f"{name} end   at {time.strftime('%H:%M:%S')}")
    return name


async def main() -> None:
    """对比 await 串行等待与不 await 直接返回的耗时差异"""
    print("=== await 版本 ===")
    start = time.time()
    r1 = await work("A", 1)
    r2 = await work("B", 1)
    print("await 总耗时:", time.time() - start)

    print("\n=== 只调用不 await 版本 ===")
    start = time.time()
    c1 = work("C", 1)
    c2 = work("D", 1)
    print("协程对象创建了，但函数体没执行")
    print("不 await 总耗时:", time.time() - start)


if __name__ == "__main__":
    asyncio.run(main())