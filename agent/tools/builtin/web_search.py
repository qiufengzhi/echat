"""内置工具：联网搜索（火山引擎豆包搜索 API）"""

from __future__ import annotations

from typing import Any, Dict, List, Optional

import httpx

from agent.tools.base import BaseTool


class WebSearchTool(BaseTool):
    """调用豆包搜索 API 返回网页搜索结果摘要"""

    name = "web_search"  # 工具名
    description = "联网搜索网页并返回相关结果摘要。用户问实时信息、新闻、天气等需要查资料的问题时使用"  # 自然语言描述
    parameters: Dict[str, Any] = {  # JSON Schema 参数定义
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "搜索关键词，尽量具体（如：上海明天天气）",
            }
        },
        "required": ["query"],
    }

    def __init__(
        self,
        api_key: str = "",
        base_url: str = "https://open.feedcoopapi.com/search_api/global_search",
        timeout: float = 10.0,
    ) -> None:
        """初始化豆包搜索工具

        api_key: 豆包搜索 API Key，从火山引擎控制台获取，为空时尝试读 DOUBAO_SEARCH_API_KEY 环境变量
        base_url: 豆包搜索接口地址，默认使用 global_search 端点
        timeout: 搜索请求超时秒数，默认 10
        """
        import os

        self._api_key = api_key or os.environ.get("DOUBAO_SEARCH_API_KEY", "")  # API Key
        self._base_url = base_url  # 搜索接口地址
        self._timeout = timeout  # 请求超时秒数

    def execute(self, query: str = "", **kwargs) -> str:
        """调用豆包搜索 API，返回网页搜索结果摘要

        query: 搜索关键词，为空时返回提示
        kwargs: 其它未声明参数，本工具忽略
        return: 格式化的搜索结果文本，含标题、摘要、URL，超长截断到 1500 字
        """
        query = (query or "").strip()
        if not query:
            return "搜索关键词不能为空"

        if not self._api_key:
            return (
                "搜索 API Key 未配置，请设置环境变量 DOUBAO_SEARCH_API_KEY "
                "或在 config.yaml 的 search.api_key 中填写密钥"
            )

        headers = {
            "Authorization": f"Bearer {self._api_key}",
            "Content-Type": "application/json",
        }
        payload = {
            "query": query,
            "count": 8,
        }

        try:
            with httpx.Client(timeout=self._timeout) as client:
                resp = client.post(self._base_url, headers=headers, json=payload)
                resp.raise_for_status()
                data = resp.json()
        except httpx.TimeoutException:
            return "搜索请求超时，请稍后重试"
        except httpx.HTTPStatusError as e:
            # 尝试提取 API 返回的错误信息
            detail = ""
            try:
                body = e.response.json()
                detail = body.get("Message", body.get("message", ""))
            except Exception:
                pass
            code = e.response.status_code
            if detail:
                return f"搜索失败（HTTP {code}: {detail}）"
            return f"搜索失败（HTTP {code}）"
        except Exception as e:
            return f"搜索失败: {e}"

        # 解析搜索结果
        results = self._parse_results(data)
        if not results:
            return f"没有找到与「{query}」相关的搜索结果"

        # 拼接并截断
        lines: List[str] = []
        for i, item in enumerate(results, 1):
            title = item.get("title", "") or ""
            url = item.get("url", "") or ""
            snippet = item.get("snippet", "") or ""
            publish_time = item.get("publish_time", "") or ""
            hostname = item.get("hostname", "") or ""
            if not title and not snippet:
                continue
            line = f"[{i}] {title}"
            if snippet:
                line += f"\n    {snippet}"
            meta_parts = []
            if hostname:
                meta_parts.append(hostname)
            if publish_time:
                # 截取日期部分（2026-08-10T17:17:05+08:00 → 2026-08-10）
                meta_parts.append(publish_time[:10])
            if url:
                meta_parts.append(url)
            if meta_parts:
                line += f"\n    {' · '.join(meta_parts)}"
            lines.append(line)

        if not lines:
            return f"没有找到与「{query}」相关的搜索结果"

        text = "\n\n".join(lines)
        if len(text) > 1500:
            text = text[:1500] + "…"
        return text

    @staticmethod
    def _parse_results(data: Dict[str, Any]) -> List[Dict[str, str]]:
        """从豆包搜索 API 响应中提取搜索结果列表

        data: API 返回的 JSON 字典
        return: 搜索结果列表，每项含 title/url/snippet/publish_time，解析失败返回空列表
        """
        # 豆包搜索 API 实际格式: {"Result": {"Documents": [...]}}
        result = data.get("Result", data.get("result", {}))
        if isinstance(result, dict):
            documents = result.get("Documents", result.get("documents", []))
        else:
            documents = []

        if not isinstance(documents, list) or len(documents) == 0:
            return []

        output: List[Dict[str, str]] = []
        for doc in documents:
            if not isinstance(doc, dict):
                continue

            # 提取文本摘要：Snippet 是数组，取所有 text 类型的 Text 字段拼接
            snippet_text = ""
            snippet_raw = doc.get("Snippet", doc.get("snippet", []))
            if isinstance(snippet_raw, list):
                parts = []
                for seg in snippet_raw:
                    if isinstance(seg, dict) and seg.get("Type", seg.get("type", "")) == "text":
                        t = seg.get("Text", seg.get("text", ""))
                        if t:
                            parts.append(t)
                snippet_text = " ".join(parts)
            elif isinstance(snippet_raw, str):
                snippet_text = snippet_raw

            # 发布时间
            doc_info = doc.get("DocumentInfo", doc.get("documentInfo", {}))
            publish_time = ""
            if isinstance(doc_info, dict):
                publish_time = doc_info.get("PublishTime", doc_info.get("publishTime", ""))

            # 来源站点名
            host_info = doc.get("HostInfo", doc.get("hostInfo", {}))
            hostname = ""
            if isinstance(host_info, dict):
                hostname = host_info.get("Hostname", host_info.get("hostname", ""))

            title = str(doc.get("Title", doc.get("title", "")) or "")
            url = str(doc.get("Url", doc.get("url", "")) or "")

            if not title and not snippet_text:
                continue

            output.append({
                "title": title,
                "url": url,
                "snippet": snippet_text,
                "publish_time": publish_time,
                "hostname": hostname,
            })
        return output
