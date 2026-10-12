"""用真实的 OpenAI SDK 验证网关的协议转换。

这是最硬的证据：不是用 curl 看字符串，而是让官方 SDK 真正解析网关的输出。
如果 SSE 格式有任何问题（缺 data: 前缀、缺空行分隔、顺序错误），
SDK 会直接抛异常，而不是像 curl 那样把坏数据原样显示出来。
"""
import sys
from openai import OpenAI

client = OpenAI(api_key="sk-merged-token", base_url="http://127.0.0.1:8120/v1")

def test_normal(model):
    """非流式：SDK 能否正常解析"""
    try:
        r = client.chat.completions.create(
            model=model,
            messages=[{"role": "system", "content": "你是助手"},
                      {"role": "user", "content": "hi"}],
        )
        content = r.choices[0].message.content or ""
        print(f"  [非流式] OK | finish={r.choices[0].finish_reason} | usage={r.usage.total_tokens} | {content[:40]}")
        return True
    except Exception as e:
        print(f"  [非流式] 失败: {type(e).__name__}: {e}")
        return False

def test_stream(model):
    """流式：SDK 能否逐 chunk 解析（最严格，能暴露 SSE 格式问题）"""
    try:
        parts = []
        finish = None
        usage = None
        for chunk in client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": "hi"}],
            stream=True,
            stream_options={"include_usage": True},
        ):
            if chunk.choices:
                if chunk.choices[0].delta.content:
                    parts.append(chunk.choices[0].delta.content)
                if chunk.choices[0].finish_reason:
                    finish = chunk.choices[0].finish_reason
            if chunk.usage:
                usage = chunk.usage.total_tokens
        text = "".join(parts)
        print(f"  [流式]   OK | finish={finish} | usage={usage} | 拼接文本={text!r}")
        return True
    except Exception as e:
        print(f"  [流式]   失败: {type(e).__name__}: {e}")
        return False

if __name__ == "__main__":
    results = {}
    for model in ["gpt-4o-mini", "claude-3-5-sonnet-20241022", "gemini-1.5-pro"]:
        print(f"\n=== {model} ===")
        results[model] = (test_normal(model), test_stream(model))
    print("\n" + "=" * 50)
    ok = all(n and s for n, s in results.values())
    print("全部通过 ✓" if ok else "存在失败 ✗")
    sys.exit(0 if ok else 1)
