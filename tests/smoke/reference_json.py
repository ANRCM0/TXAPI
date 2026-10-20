"""Bounded Chat JSON fixture/client for the fixed-source reference comparison."""

import contextlib
import hashlib
import http.client
import json
import socket
import threading
import time
import uuid

from stream_baseline import Fixture as StreamFixture, FixtureHandler, TEXT, UPSTREAM_KEY, clock


def document(tag):
    return {"id": "chatcmpl_" + tag, "object": "chat.completion", "created": 1, "model": "baseline-model",
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "".join(TEXT)}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
            "x_fixture": {"opaque": [1, 0, False], "note": "preserve unknown provider fields"}}


def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode()


class Fixture(StreamFixture):
    daemon_threads = False

    def __init__(self, delay):
        super().__init__(delay)
        self.RequestHandlerClass = Handler

    def get_request(self):
        connection, address = super().get_request()
        connection.settimeout(5)
        return connection, address


class Handler(FixtureHandler):
    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        tag, mode = payload["messages"][0]["content"].split(":", 1)
        record = {"events": [], "finished": False, "error": None, "mode": mode,
                  "http": self.request_version, "peer": f"{self.client_address[0]}:{self.client_address[1]}"}
        with self.server.lock:
            self.server.records.setdefault(tag, []).append(record)
        try:
            if (self.path != "/v1/chat/completions" or payload.get("stream") is not False
                    or self.headers.get("Authorization") != "Bearer " + UPSTREAM_KEY):
                self.send_error(401)
                return
            value = document(tag)
            if mode == "provider_error":
                value = {"error": {"type": "server_error", "message": "synthetic provider error"}}
            elif mode == "invalid_usage":
                value["usage"]["total_tokens"] = 19
            body = encoded(value)
            if mode == "timeout":
                pieces = [b" "] * 100 + [body]
                delay = .01
            elif mode == "provider_error":
                pieces, delay = [body], self.server.delay
            else:
                # The first write ends inside a multibyte UTF-8 character.
                split = body.index(TEXT[1].encode()) + 1
                pieces, delay = [body[:split], body[split:]], self.server.delay
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(sum(map(len, pieces))))
            self.end_headers()
            for index, piece in enumerate(pieces):
                started = clock()
                self.wfile.write(piece)
                self.wfile.flush()
                record["events"].append({"index": index, "write_start_ns": started, "flush_return_ns": clock()})
                if mode == "truncated":
                    self.close_connection = True
                    return
                if index < len(pieces) - 1 and self.server.stop_event.wait(delay):
                    return
        except (OSError, ConnectionError) as error:
            record["error"] = type(error).__name__
        finally:
            record["finished"] = True


def call(connection, key, fixture, seconds=5, mode="normal"):
    tag = uuid.uuid4().hex
    sample = {"fixture_id": tag, "success": False, "failure": None, "headers_ns": None,
              "first_body_byte_ns": None, "first_effective_text_ns": None, "events": [], "http_status": None}
    started = clock()
    expired = threading.Event()
    timer, response, received = None, None, None
    try:
        connection.timeout = seconds
        reused = connection.sock is not None
        if not reused:
            connection.connect()
        exact_socket = connection.sock
        exact_socket.settimeout(seconds)
        sample.update(client_local_port=exact_socket.getsockname()[1], reused_client_socket=reused)

        def interrupt():
            expired.set()
            with contextlib.suppress(OSError):
                exact_socket.shutdown(socket.SHUT_RDWR)
            exact_socket.close()

        timer = threading.Timer(max(.001, seconds - (clock() - started) / 1e9), interrupt)
        timer.daemon = True
        timer.start()
        body = {"model": "baseline-model", "stream": False,
                "messages": [{"role": "user", "content": tag + ":" + mode}]}
        connection.request("POST", "/v1/chat/completions", json.dumps(body),
                           {"Authorization": "Bearer " + key, "Content-Type": "application/json"})
        response = connection.getresponse()
        sample.update(headers_ns=clock() - started, http_status=response.status, http_version=response.version)
        if response.status != 200:
            response.read(65536)
            raise ValueError("http_rejected")
        if response.version != 11 or not response.getheader("Content-Type", "").startswith("application/json"):
            raise ValueError("response_protocol_mismatch")
        raw = bytearray()
        while data := response.read1(4096):
            if sample["first_body_byte_ns"] is None:
                sample["first_body_byte_ns"] = clock() - started
            raw.extend(data)
            if len(raw) > 1 << 20:
                raise ValueError("response_exceeds_limit")
        value = json.loads(raw.decode("utf-8"))
        if not isinstance(value, dict):
            raise ValueError("response_not_object")
        if value.get("error"):
            raise ValueError("provider_error")
        if value != document(tag):
            raise ValueError("json_fields_or_usage_mismatch")
        received = clock()
        sample.update(success=True, first_effective_text_ns=received - started,
                      valid_json_document_ns=received - started, observed_json_documents=1,
                      observed_usage=value["usage"], preserved_unknown_extension=True,
                      raw_byte_identity=raw == encoded(document(tag)), response_sha256=hashlib.sha256(raw).hexdigest())
    except (OSError, ValueError, KeyError, TypeError, http.client.HTTPException) as error:
        sample["failure"] = type(error).__name__ if not isinstance(error, ValueError) else str(error)[:80]
        connection.close()
    finally:
        if timer is not None:
            timer.cancel()
            timer.join()
        if response is not None:
            response.close()
        if expired.is_set() or clock() - started >= int(seconds * 1e9):
            sample.update(success=False, failure="absolute_deadline")
            connection.close()
    sample["elapsed_ns"] = clock() - started
    deadline = time.monotonic() + 2
    while True:
        with fixture.lock:
            attempts = fixture.records.get(tag, [])
            drained = all(item["finished"] for item in attempts)
        if drained or time.monotonic() >= deadline:
            break
        time.sleep(.005)
    with fixture.lock:
        attempts = fixture.records.get(tag, [])
        sample.update(upstream_attempts=len(attempts), drain_failure=not all(a["finished"] for a in attempts),
                      upstream_peers=[a["peer"] for a in attempts], upstream_http=[a["http"] for a in attempts])
        sent = list(attempts[0]["events"]) if len(attempts) == 1 else []
    if received is not None and sent:
        sample["events"].append({"document": 0,
                                 "latency_lower_ns": max(0, received - sent[-1]["flush_return_ns"]),
                                 "latency_upper_ns": received - sent[-1]["write_start_ns"]})
    if sample["success"] and (len(attempts) != 1 or sample["drain_failure"] or len(sample["events"]) != 1
                              or sample["upstream_http"] != ["HTTP/1.1"]):
        sample.update(success=False, failure="attempt_drain_or_document_mismatch")
    return sample
