"""Optional OpenAI SDK parsing check; defaults match tests/configs/protocols.yaml.

Start the local gateway and fake providers first. This is not a complete
cross-protocol conformance test (see docs/history/acceptance-s0-s5.md, S0).
"""
import argparse
import os
import sys


def test_normal(client, model):
    try:
        response = client.chat.completions.create(
            model=model,
            messages=[{"role": "system", "content": "你是助手"},
                      {"role": "user", "content": "hi"}],
        )
        content = response.choices[0].message.content or ""
        assert content, "empty content"
        assert response.choices[0].finish_reason, "missing finish_reason"
        assert response.usage is not None, "missing usage"
        print(f"  normal: OK | finish={response.choices[0].finish_reason} | usage={response.usage.total_tokens}")
        return True
    except Exception as error:
        print(f"  normal: FAIL | {type(error).__name__}: {error}")
        return False


def test_stream(client, model):
    try:
        parts, finish, usage = [], None, None
        with client.chat.completions.create(
            model=model, messages=[{"role": "user", "content": "hi"}],
            stream=True, stream_options={"include_usage": True},
        ) as stream:
            for chunk in stream:
                if chunk.choices:
                    if chunk.choices[0].delta.content:
                        parts.append(chunk.choices[0].delta.content)
                    if chunk.choices[0].finish_reason:
                        finish = chunk.choices[0].finish_reason
                if chunk.usage:
                    usage = chunk.usage.total_tokens
        assert "".join(parts), "empty stream"
        assert finish, "missing finish_reason"
        assert usage is not None, "missing usage"
        print(f"  stream: OK | finish={finish} | usage={usage}")
        return True
    except Exception as error:
        print(f"  stream: FAIL | {type(error).__name__}: {error}")
        return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8090/v1")
    parser.add_argument("--api-key", default=os.getenv("GATEWAY_API_KEY", "sk-test-p4"))
    parser.add_argument("--models", nargs="+", default=["gpt-4o-mini", "claude-3-5-sonnet-20241022", "gemini-1.5-pro"])
    args = parser.parse_args()
    try:
        from openai import OpenAI
    except ImportError:
        parser.exit(2, "Install the optional 'openai' package in a virtual environment; see docs/DEVELOPMENT.md.\n")
    results = []
    with OpenAI(api_key=args.api_key, base_url=args.base_url, max_retries=0, timeout=30) as client:
        for model in args.models:
            print(model)
            results.extend((test_normal(client, model), test_stream(client, model)))
    return 0 if all(results) else 1


if __name__ == "__main__":
    sys.exit(main())
