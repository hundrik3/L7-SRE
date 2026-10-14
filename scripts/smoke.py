#!/usr/bin/env python3
"""Exercise the compiled server with three local backends. Standard library only."""
import concurrent.futures
import contextlib
import http.server
import os
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def get(url, headers=None):
    try:
        response = opener.open(urllib.request.Request(url, headers=headers or {}), timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.read().decode(), response.headers


def wait(check):
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.05)
    raise AssertionError("readiness condition did not become true")


class Backend(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path == "/slow":
            self.server.started.set()
            time.sleep(0.4)
        body = (self.server.name + "\n").encode()
        self.send_response(self.server.status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


@contextlib.contextmanager
def process(binary, backends, **settings):
    data_port, admin_port = port(), port()
    values = dict(os.environ)
    values.update(LISTEN_ADDR=f"127.0.0.1:{data_port}", ADMIN_ADDR=f"127.0.0.1:{admin_port}",
                  BACKENDS=",".join(backends), HEALTH_INTERVAL="100ms", HEALTH_TIMEOUT="200ms",
                  RATE_LIMIT_RPS="100", RATE_LIMIT_BURST="200", TRUSTED_PROXY_CIDRS="",
                  NO_PROXY="127.0.0.1,localhost", no_proxy="127.0.0.1,localhost")
    values.update(settings)
    with tempfile.TemporaryFile() as logs:
        child = subprocess.Popen([binary], env=values, stdout=logs, stderr=logs)
        try:
            admin = f"http://127.0.0.1:{admin_port}"
            wait(lambda: get(admin + "/readyz")[0] == 200)
            yield child, f"http://127.0.0.1:{data_port}", admin
        except Exception:
            logs.seek(0)
            print(logs.read().decode(), file=sys.stderr)
            raise
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()


def main():
    binary = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else "bin/l7lb")
    servers = []
    try:
        for i in range(3):
            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Backend)
            server.name, server.status, server.started = f"backend{i+1}", 200, threading.Event()
            threading.Thread(target=server.serve_forever, daemon=True).start()
            servers.append(server)
        urls = [f"http://127.0.0.1:{server.server_port}" for server in servers]
        with process(binary, urls) as (child, data, admin):
            replies = [get(data + "/")[1].strip() for _ in range(6)]
            assert replies == ["backend1", "backend2", "backend3"] * 2, replies
            servers[0].status = 503
            assert get(data + "/")[0] == 200
            metric = f'backend_status{{backend_url="{urls[0]}"}} 0'
            wait(lambda: metric in get(admin + "/metrics")[1])
            assert all(get(data + "/")[1].strip() != "backend1" for _ in range(3))
            servers[0].status = 200
            wait(lambda: metric[:-1] + "1" in get(admin + "/metrics")[1])
            assert "backend1" in [get(data + "/")[1].strip() for _ in range(3)]
            for server in servers:
                server.status = 503
            wait(lambda: get(admin + "/readyz")[0] == 503)
            assert get(data + "/")[0] == 503
            for server in servers:
                server.status = 200
            wait(lambda: get(admin + "/readyz")[0] == 200)
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                result = pool.submit(get, data + "/slow")
                wait(lambda: any(server.started.is_set() for server in servers))
                child.send_signal(signal.SIGTERM)
                assert result.result()[0] == 200
                assert child.wait(timeout=5) == 0
            print("PASS: round robin, failover, recovery, readiness, metrics, graceful shutdown")
        with process(binary, urls, RATE_LIMIT_RPS="1", RATE_LIMIT_BURST="2") as (_, data, admin):
            assert get(data + "/")[0] == 200
            assert get(data + "/")[0] == 200
            status, _, headers = get(data + "/", {"X-Forwarded-For": "203.0.113.99"})
            assert status == 429 and int(headers["Retry-After"]) >= 1
            assert "rate_limit_dropped_total 1" in get(admin + "/metrics")[1]
            assert get(admin + "/healthz")[0] == 200
            print("PASS: rate limit, Retry-After, spoof protection, admin isolation")
    finally:
        for server in servers:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    main()
