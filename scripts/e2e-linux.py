#!/usr/bin/env python3
"""Native acceptance run for OCcodeBridge on Linux.

One real CLIProxyAPI host is started with the freshly built occodebridge.so and
a complete request is driven through it, from plugin load down to what the fake
OpenCode Go upstream received. It is the Linux port of scripts/e2e-native.ps1:
the same 21 assertions, the same isolated runtime layout, the same local fake
upstream (scripts/fake-opencode-upstream.py).

Everything happens under one runtime directory (default
<repo>/.runtime/occodebridge-e2e, override with --root or
OCCODEBRIDGE_E2E_ROOT): the plugin directory, the CPA config/auth/logs, the
plugin data directory and the fake upstream. No request leaves 127.0.0.1, no
real key is used, and every process this script starts is stopped at the end,
including on failure and on SIGINT/SIGTERM.

    python3 scripts/e2e-linux.py
    python3 scripts/e2e-linux.py --cpa-exe /opt/cpa-v8.0.67/cli-proxy-api --root /tmp/occodebridge-run
    python3 scripts/e2e-linux.py --no-build   # reuse dist/occodebridge.so as is

The CPA executable defaults to /opt/cpa-v8.0.67/cli-proxy-api and is only
executed, never copied or modified. Requires Python 3 (standard library only), a
C toolchain for the plugin build that scripts/build-linux.sh performs, and a
Python 3 interpreter for the fake upstream (sys.executable is reused).
"""

import argparse
import hashlib
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

SCRIPTS_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPTS_DIR.parent
FAKE_UPSTREAM = SCRIPTS_DIR / "fake-opencode-upstream.py"
BUILD_SCRIPT = SCRIPTS_DIR / "build-linux.sh"
DEFAULT_CPA_EXE = "/opt/cpa-v8.0.67/cli-proxy-api"
DEFAULT_ROOT = REPO_ROOT / ".runtime" / "occodebridge-e2e"
PLUGIN_ID = "occodebridge"
PLUGIN_VERSION = "0.1.0"

# A test process must not route its own HTTP calls through an ambient proxy, so
# every request here uses an opener with an empty proxy map (the equivalent of
# -NoProxy in scripts/e2e-native.ps1).
_opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


class Interrupted(Exception):
    """Raised from the SIGINT/SIGTERM handler so the finally blocks can clean up."""


class Response:
    def __init__(self, status, text):
        self.status = status
        self.text = text
        try:
            self.json = json.loads(text) if text else None
        except json.JSONDecodeError:
            self.json = None


def http_request(method, url, body=None, headers=None, timeout=60):
    request_headers = dict(headers or {})
    data = None
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        request_headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=request_headers, method=method)
    try:
        with _opener.open(request, timeout=timeout) as response:
            return Response(response.status, response.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as error:
        return Response(error.code, error.read().decode("utf-8", "replace"))


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def port_open(port):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.3):
            return True
    except OSError:
        return False


def wait_for(probe, seconds, interval=0.5):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = probe()
        if value:
            return value
        time.sleep(interval)
    return None


def mask_secret(value):
    """Keeps a secret out of the console: only a length and a short prefix survive."""
    if not value:
        return "<empty>"
    if len(value) <= 8:
        return "***"
    return value[:8] + "***(" + str(len(value)) + " chars)"


def file_sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


class Acceptance:
    def __init__(self, args):
        self.args = args
        self.root = Path(args.root).resolve()
        self.cpa_exe = Path(args.cpa_exe).resolve()
        self.bin_dir = self.root / "bin"
        self.log_dir = self.root / "logs"
        self.plugin_dir = self.root / "plugins"
        self.auth_dir = self.root / "auths"
        self.data_dir = self.root / "data"
        self.state_dir = self.root / "state"
        self.config_path = self.root / "config.yaml"
        self.capture_path = self.state_dir / "upstream-capture.jsonl"

        self.client_key = "occodebridge-e2e-client-" + os.urandom(16).hex()
        self.management_key = "occodebridge-e2e-mgmt-" + os.urandom(16).hex()
        self.credential_key = "occodebridge-e2e-upstream-" + os.urandom(16).hex()
        self.alias = "e2e-deepseek-flash"
        self.upstream_model = "deepseek-v4.1-flash"

        self.results = []
        self.processes = []
        self.log_handles = []
        self.auth_index = ""
        self.runtime_root = self.root

    # -- output -----------------------------------------------------------

    @staticmethod
    def write_line(text):
        sys.stdout.write(text + "\n")
        sys.stdout.flush()

    def add_result(self, name, ok, detail):
        label = "PASS" if ok else "FAIL"
        self.write_line("[{0}] {1} - {2}".format(label, name, detail))
        self.results.append({"name": name, "ok": bool(ok), "detail": detail})

    def record(self):
        return {entry["name"]: entry for entry in self.results}

    # -- process plumbing -------------------------------------------------

    def spawn(self, argv, log_name, env=None):
        """Starts a child with its stdout/stderr redirected into the log directory."""
        stdout = open(self.log_dir / (log_name + "-stdout.log"), "wb")
        stderr = open(self.log_dir / (log_name + "-stderr.log"), "wb")
        self.log_handles.extend([stdout, stderr])
        process = subprocess.Popen(argv, cwd=str(self.runtime_root), env=env, stdout=stdout, stderr=stderr)
        self.processes.append(process)
        return process

    def stop_processes(self):
        self.write_line("== stopping processes")
        for process in reversed(self.processes):
            try:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=15)
            except OSError as error:
                self.write_line("could not stop pid {0}: {1}".format(process.pid, error))
        self.processes = []
        for handle in self.log_handles:
            try:
                handle.close()
            except OSError:
                pass
        self.log_handles = []

    # -- environment ------------------------------------------------------

    def cpa_environment(self, dead_proxy_port):
        """The CPA child gets a loopback-only environment.

        No ambient proxy may sit between the plugin and the fake upstream, and the
        management key arrives through MANAGEMENT_PASSWORD instead of a bcrypt hash
        in the config file. HTTP(S)_PROXY points at a closed loopback port so any
        request the plugin still aims at a real vendor host (the OpenCode Go usage
        endpoint is not part of the fake upstream) fails locally instead of
        reaching the internet.
        """
        env = dict(os.environ)
        for name in ("ALL_PROXY", "all_proxy"):
            env.pop(name, None)
        dead_proxy = "http://127.0.0.1:{0}".format(dead_proxy_port)
        for name in ("HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"):
            env[name] = dead_proxy
        no_proxy = "127.0.0.1,localhost,::1"
        env["NO_PROXY"] = no_proxy
        env["no_proxy"] = no_proxy
        env["MANAGEMENT_PASSWORD"] = self.management_key
        return env

    def config_yaml(self, cpa_port, upstream_port):
        lines = [
            "# Generated by scripts/e2e-linux.py for one local acceptance run.",
            "config-version: 8",
            "server:",
            '    host: "127.0.0.1"',
            "    port: {0}".format(cpa_port),
            "    response-compression:",
            "        enabled: false",
            "plugins:",
            "    enabled: true",
            '    dir: "{0}"'.format(self.plugin_dir.as_posix()),
            "    configs:",
            "        {0}:".format(PLUGIN_ID),
            "            enabled: true",
            "            priority: 0",
            '            data_dir: "{0}"'.format(self.data_dir.as_posix()),
            "            # Native non-stream keeps the fake upstream non-stream branch exercised.",
            "            nonstream_mode: native",
            "            base_url_overrides:",
            '                opencode-go: "http://127.0.0.1:{0}/v1"'.format(upstream_port),
            "routing:",
            '    strategy: "round-robin"',
            "    session-affinity: false",
            "    cooldown:",
            "        disable-cooling: true",
            "    retry:",
            "        request-retry: 0",
            "        max-retry-credentials: 1",
            "access:",
            "    api-keys:",
            '        - "{0}"'.format(self.client_key),
            "oauth:",
            '    auth-dir: "{0}"'.format(self.auth_dir.as_posix()),
            "management:",
            "    allow-remote: false",
            "    disable-control-panel: true",
            "    disable-auto-update-panel: true",
            "observability:",
            "    logs:",
            "        logging-to-file: true",
            "        request-log: false",
            "        debug: false",
        ]
        return "\n".join(lines) + "\n"

    # -- steps ------------------------------------------------------------

    def prepare_runtime(self):
        self.runtime_root.mkdir(parents=True, exist_ok=True)
        for directory in (self.bin_dir, self.log_dir, self.state_dir):
            directory.mkdir(parents=True, exist_ok=True)

        # Every run starts from empty plugin state: auth files, plugin data and the
        # plugin directory are rebuilt, so a rerun cannot lean on the previous one.
        for directory in (self.plugin_dir, self.auth_dir, self.data_dir):
            if directory.exists():
                shutil.rmtree(directory)
            directory.mkdir(parents=True, exist_ok=True)
        for log_file in self.log_dir.glob("*.log"):
            log_file.unlink(missing_ok=True)

    def build_plugin(self):
        self.write_line("== build the plugin shared library")
        if self.args.no_build:
            source = Path(self.args.plugin_so).resolve() if self.args.plugin_so else REPO_ROOT / "dist" / "occodebridge.so"
            if not source.is_file():
                raise RuntimeError("--no-build needs an existing plugin artifact, missing " + str(source))
            self.write_line("reusing plugin artifact " + str(source))
        else:
            completed = subprocess.run(["bash", str(BUILD_SCRIPT)], cwd=str(REPO_ROOT))
            if completed.returncode != 0:
                raise RuntimeError("scripts/build-linux.sh failed with exit code {0}".format(completed.returncode))
            source = REPO_ROOT / "dist" / "occodebridge.so"
            if not source.is_file():
                raise RuntimeError("scripts/build-linux.sh reported success but " + str(source) + " is missing")

        target = self.plugin_dir / "occodebridge.so"
        shutil.copyfile(source, target)
        self.write_line(
            "plugin .so: {0} bytes sha256={1}".format(target.stat().st_size, file_sha256(target))
        )

    def resolve_cpa(self):
        self.write_line("== resolve the CPA executable")
        if not self.cpa_exe.is_file():
            raise RuntimeError("CPA executable not found: {0} (pass --cpa-exe or set OCCODEBRIDGE_CPA_EXE)".format(self.cpa_exe))
        if not os.access(str(self.cpa_exe), os.X_OK):
            raise RuntimeError("CPA executable is not executable: {0}".format(self.cpa_exe))
        self.write_line(
            "cpa exe: {0} bytes sha256={1}".format(self.cpa_exe.stat().st_size, file_sha256(self.cpa_exe))
        )

    def run(self):
        self.prepare_runtime()
        self.build_plugin()
        self.resolve_cpa()

        cpa_port = free_port()
        upstream_port = free_port()
        dead_proxy_port = free_port()
        base = "http://127.0.0.1:{0}".format(cpa_port)
        management_headers = {"Authorization": "Bearer " + self.management_key}
        client_headers = {"Authorization": "Bearer " + self.client_key}
        self.write_line("cpa={0} upstream=http://127.0.0.1:{1}".format(base, upstream_port))
        self.write_line(
            "client key: {0}; management key: {1}; test upstream key: {2}".format(
                mask_secret(self.client_key), mask_secret(self.management_key), mask_secret(self.credential_key)
            )
        )

        self.write_line("== start the fake OpenCode Go upstream")
        upstream = self.spawn(
            [sys.executable, str(FAKE_UPSTREAM), "--port", str(upstream_port), "--capture", str(self.capture_path)],
            "upstream",
        )
        upstream_ready = wait_for(lambda: port_open(upstream_port), 30)
        if not upstream_ready:
            raise RuntimeError(
                "the fake upstream did not listen on 127.0.0.1:{0}; see {1}".format(
                    upstream_port, self.log_dir / "upstream-stderr.log"
                )
            )

        self.write_line("== write the isolated CPA config")
        self.config_path.write_text(self.config_yaml(cpa_port, upstream_port), encoding="utf-8")

        self.write_line("== start CPA")
        cpa = self.spawn(
            [str(self.cpa_exe), "--config", str(self.config_path), "--local-model"],
            "cpa",
            env=self.cpa_environment(dead_proxy_port),
        )
        self.write_line("cpa pid={0}".format(cpa.pid))

        def healthy():
            if cpa.poll() is not None:
                return False
            try:
                return http_request("GET", base + "/healthz", timeout=5).status == 200
            except OSError:
                return False

        started = wait_for(healthy, 60)
        self.add_result(
            "cpa.starts",
            bool(started),
            "GET /healthz -> 200 (pid {0})".format(cpa.pid)
            if started
            else "CPA did not answer /healthz; see {0}".format(self.log_dir),
        )
        if not started:
            raise RuntimeError("CPA did not start; the remaining assertions cannot run")

        self.assert_plugin_loaded(base, management_headers)
        self.assert_credentials_and_models(base, management_headers, client_headers)
        self.assert_chat(base, client_headers)
        self.assert_plugin_log(base, management_headers)
        self.assert_upstream_capture()
        self.assert_quota(base, management_headers, client_headers)

    def assert_plugin_loaded(self, base, management_headers):
        # 1. The host loaded the plugin: the console resource is served by the
        #    plugin resource route, and the plugin answers its own management API.
        console = http_request("GET", base + "/v0/resource/plugins/" + PLUGIN_ID + "/console")
        console_served = console.status == 200 and 'id="credentialsTab"' in console.text
        placeholders_replaced = "__OCCODEBRIDGE_API_BASE__" not in console.text
        self.add_result(
            "plugin.console",
            console_served and placeholders_replaced,
            "GET /v0/resource/plugins/{0}/console -> {1}, {2} bytes, placeholders replaced={3}".format(
                PLUGIN_ID, console.status, len(console.text), placeholders_replaced
            ),
        )

        status = http_request("GET", base + "/v0/management/" + PLUGIN_ID + "/status", headers=management_headers)
        version = str(status.json.get("version", "")) if isinstance(status.json, dict) else ""
        self.add_result(
            "plugin.loaded",
            status.status == 200 and version == PLUGIN_VERSION,
            "GET /v0/management/{0}/status -> {1} version={2}".format(PLUGIN_ID, status.status, version),
        )

    def assert_credentials_and_models(self, base, management_headers, client_headers):
        # 2. Import one credential through the plugin's own management API and add
        #    the model mapping a client asks for.
        imported = http_request(
            "POST",
            base + "/v0/management/" + PLUGIN_ID + "/credentials",
            headers=management_headers,
            body={
                "label": "e2e OpenCode Go",
                "api_key": self.credential_key,
                "vendor": "opencode-go",
                "priority": 50,
            },
        )
        credential_id = ""
        if isinstance(imported.json, dict):
            credential_id = str(imported.json.get("id", ""))
        self.add_result(
            "credential.import",
            imported.status == 201 and bool(credential_id),
            "POST /credentials -> {0} id={1}".format(imported.status, credential_id),
        )

        models = http_request(
            "PUT",
            base + "/v0/management/" + PLUGIN_ID + "/models",
            headers=management_headers,
            body={"models": [{"id": self.alias, "upstream_id": "opencode-go/" + self.upstream_model}]},
        )
        self.add_result(
            "models.mapping",
            models.status == 200,
            "PUT /models -> {0} ({1} -> opencode-go/{2})".format(models.status, self.alias, self.upstream_model),
        )

        def advertises_alias():
            response = http_request("GET", base + "/v1/models", headers=client_headers, timeout=10)
            if response.status != 200 or not isinstance(response.json, dict):
                return None
            ids = [str(entry.get("id", "")) for entry in response.json.get("data") or []]
            return response if self.alias in ids else None

        advertised = wait_for(advertises_alias, 30, interval=1.0)
        self.add_result(
            "cpa.models",
            bool(advertised),
            "GET /v1/models exposes " + self.alias
            if advertised
            else "GET /v1/models never exposed " + self.alias,
        )

        def auth_record():
            response = http_request("GET", base + "/v0/management/auth-files", headers=management_headers, timeout=10)
            if response.status != 200 or not isinstance(response.json, dict):
                return None
            for entry in response.json.get("files") or []:
                if str(entry.get("provider", "")) == PLUGIN_ID and str(entry.get("auth_index", "")):
                    return entry
            return None

        auth = wait_for(auth_record, 30, interval=1.0)
        auth_index = str(auth.get("auth_index", "")) if auth else ""
        self.add_result(
            "cpa.auth-record",
            bool(auth_index),
            "auth-files lists a {0} record with auth_index".format(PLUGIN_ID)
            if auth_index
            else "no {0} auth record appeared".format(PLUGIN_ID),
        )
        self.auth_index = auth_index

    def assert_chat(self, base, client_headers):
        # 3. One non-stream and one stream client request through CPA.
        chat_body = {
            "model": self.alias,
            "messages": [{"role": "user", "content": "Reply with the fake upstream answer."}],
        }
        non_stream = http_request(
            "POST", base + "/v1/chat/completions", headers=client_headers, body=chat_body, timeout=120
        )
        non_stream_text = ""
        if isinstance(non_stream.json, dict) and non_stream.json.get("choices"):
            message = non_stream.json["choices"][0].get("message") or {}
            non_stream_text = str(message.get("content", ""))
        non_stream_ok = non_stream.status == 200 and "FAKE-UPSTREAM-OK" in non_stream_text
        usage_present = isinstance(non_stream.json, dict) and non_stream.json.get("usage") is not None
        self.add_result(
            "chat.nonstream",
            non_stream_ok,
            "POST /v1/chat/completions (non-stream) -> {0} content={1} usage_present={2}".format(
                non_stream.status, non_stream_text, usage_present
            ),
        )

        stream_body = dict(chat_body, stream=True)
        stream = http_request(
            "POST", base + "/v1/chat/completions", headers=client_headers, body=stream_body, timeout=120
        )
        stream_ok = stream.status == 200 and "FAKE-UPSTREAM-OK" in stream.text and "[DONE]" in stream.text
        self.add_result(
            "chat.stream",
            stream_ok,
            "POST /v1/chat/completions (stream) -> {0}, contains answer={1}, [DONE]={2}".format(
                stream.status, "FAKE-UPSTREAM-OK" in stream.text, "[DONE]" in stream.text
            ),
        )

    def assert_plugin_log(self, base, management_headers):
        # 4. The plugin request log records the request and the plan allowance.
        log_response = http_request(
            "GET", base + "/v0/management/" + PLUGIN_ID + "/logs?limit=10", headers=management_headers
        )
        entries = []
        if isinstance(log_response.json, dict):
            entries = [entry for entry in log_response.json.get("items") or [] if isinstance(entry, dict)]
        successful = [entry for entry in entries if int(entry.get("status") or 0) == 200]
        with_allowance = [entry for entry in successful if entry.get("allowance") is not None]
        first = successful[0] if successful else {}
        allowance_text = json.dumps(with_allowance[0]["allowance"], separators=(",", ":")) if with_allowance else ""
        prompt_tokens = int(first.get("prompt_tokens") or 0)
        completion_tokens = int(first.get("completion_tokens") or 0)
        upstream_model = str(first.get("upstream_model", ""))
        self.add_result(
            "plugin.request-log",
            len(successful) >= 2 and len(with_allowance) >= 1 and prompt_tokens > 0 and completion_tokens > 0,
            "{0} logged request(s), {1} with allowance, tokens={2}/{3}, upstream_model={4}".format(
                len(successful), len(with_allowance), prompt_tokens, completion_tokens, upstream_model
            ),
        )
        self.add_result("plugin.log-allowance", allowance_text != "", "logged allowance: " + allowance_text)

    def assert_upstream_capture(self):
        # 5. What the fake upstream actually received: our test key, a stable
        #    session header, the plugin User-Agent, and the model without the prefix.
        captured = []
        if self.capture_path.is_file():
            for line in self.capture_path.read_text(encoding="utf-8").splitlines():
                if not line.strip():
                    continue
                try:
                    captured.append(json.loads(line))
                except json.JSONDecodeError:
                    continue
        self.add_result(
            "upstream.captured",
            len(captured) >= 2,
            "fake upstream received {0} request(s)".format(len(captured)),
        )
        if not captured:
            return

        def header(entry, name):
            wanted = name.lower()
            for key, value in (entry.get("headers") or {}).items():
                if key.lower() == wanted:
                    return str(value)
            return ""

        authorization = header(captured[0], "Authorization")
        session = header(captured[0], "x-opencode-session")
        user_agent = header(captured[0], "User-Agent")
        sent_model = str((captured[0].get("body") or {}).get("model", ""))
        self.add_result(
            "upstream.authorization",
            authorization == "Bearer " + self.credential_key,
            "Authorization={0} matches the test key".format(
                mask_secret(authorization[len("Bearer "):] if authorization.startswith("Bearer ") else authorization)
            ),
        )
        self.add_result(
            "upstream.session-header",
            len(session) > 0,
            "x-opencode-session=" + session,
        )
        self.add_result(
            "upstream.user-agent",
            user_agent.startswith("OCcodeBridge/" + PLUGIN_VERSION),
            "User-Agent=" + user_agent,
        )
        self.add_result(
            "upstream.model-prefix",
            sent_model == self.upstream_model,
            "body.model={0} (vendor prefix removed)".format(sent_model),
        )

        sessions = {header(entry, "x-opencode-session") for entry in captured}
        sessions.discard("")
        self.add_result(
            "upstream.stable-session",
            len(sessions) == 1,
            "same session id across requests={0}".format(len(sessions) == 1),
        )

        stream_flags = [bool((entry.get("body") or {}).get("stream")) for entry in captured]
        self.add_result(
            "upstream.both-modes",
            True in stream_flags and False in stream_flags,
            "upstream saw stream={0}".format(
                ",".join("true" if flag else "false" for flag in stream_flags)
            ),
        )

    def assert_quota(self, base, management_headers, client_headers):
        # 6. CPA's normalized quota route for the same credential. The fake upstream
        #    does not serve the vendor usage endpoint, so the only acceptable answer
        #    is an explicit error, and the host must stay healthy afterwards.
        if not self.auth_index:
            return
        quota = http_request(
            "POST",
            base + "/v8/management/plugins/" + PLUGIN_ID + "/quota",
            headers=management_headers,
            body={"auth_index": self.auth_index},
            timeout=120,
        )
        if isinstance(quota.json, dict):
            raw_error = quota.json.get("error")
            quota_error = raw_error if isinstance(raw_error, str) else json.dumps(raw_error, ensure_ascii=False)
            quota_error = quota_error or ""
        else:
            quota_error = quota.text
        self.add_result(
            "quota.explicit-error",
            quota.status >= 400 and quota_error.strip() != "",
            "POST /v8/management/plugins/{0}/quota -> {1} error={2}".format(PLUGIN_ID, quota.status, quota_error.strip()),
        )

        after = http_request(
            "POST",
            base + "/v1/chat/completions",
            headers=client_headers,
            body={
                "model": self.alias,
                "messages": [{"role": "user", "content": "Reply with the fake upstream answer."}],
            },
            timeout=120,
        )
        after_text = ""
        if isinstance(after.json, dict) and after.json.get("choices"):
            message = after.json["choices"][0].get("message") or {}
            after_text = str(message.get("content", ""))
        self.add_result(
            "quota.host-survived",
            after.status == 200 and "FAKE-UPSTREAM-OK" in after_text,
            "a chat request after the quota error -> {0}".format(after.status),
        )

    def assert_host_log(self):
        # The host writes its startup log while it holds the redirection files open,
        # so this one assertion is evaluated after both processes have stopped.
        log_text = ""
        for log_file in sorted(self.log_dir.glob("*.log")):
            try:
                log_text += log_file.read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
        loaded = re.search(r"pluginhost: plugin loaded plugin_id=" + PLUGIN_ID, log_text, re.IGNORECASE) is not None
        registered = re.search(r"pluginhost: plugin registered plugin_id=" + PLUGIN_ID, log_text, re.IGNORECASE) is not None
        self.add_result(
            "plugin.host-log",
            loaded and registered,
            "CPA log records plugin load={0} register={1}".format(loaded, registered),
        )


def parse_args(argv):
    parser = argparse.ArgumentParser(description="Native acceptance run for OCcodeBridge on Linux.")
    parser.add_argument(
        "--cpa-exe",
        default=os.environ.get("OCCODEBRIDGE_CPA_EXE") or DEFAULT_CPA_EXE,
        help="CPA executable to run (default: %(default)s, or $OCCODEBRIDGE_CPA_EXE)",
    )
    parser.add_argument(
        "--root",
        default=os.environ.get("OCCODEBRIDGE_E2E_ROOT") or str(DEFAULT_ROOT),
        help="isolated runtime directory (default: %(default)s, or $OCCODEBRIDGE_E2E_ROOT)",
    )
    parser.add_argument(
        "--no-build",
        action="store_true",
        help="reuse dist/occodebridge.so instead of running scripts/build-linux.sh",
    )
    parser.add_argument(
        "--plugin-so",
        metavar="PATH",
        default=None,
        help="with --no-build, the plugin .so to install instead of dist/occodebridge.so",
    )
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv or sys.argv[1:])
    acceptance = Acceptance(args)

    def handle_signal(signum, _frame):
        raise Interrupted("received signal {0}".format(signum))

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    exit_code = 0
    failure = None
    try:
        acceptance.run()
    except Interrupted as error:
        failure = str(error)
    except (RuntimeError, OSError, subprocess.SubprocessError) as error:
        failure = str(error)
    finally:
        try:
            acceptance.stop_processes()
        except Interrupted:
            acceptance.write_line("cleanup interrupted; forcing the remaining children down")

    if failure:
        acceptance.write_line("ERROR: " + failure)

    acceptance.assert_host_log()

    summary = acceptance.results
    failed = [entry for entry in summary if not entry["ok"]]
    acceptance.write_line("")
    acceptance.write_line("== summary")
    for entry in summary:
        acceptance.write_line(
            "  [{0}] {1} - {2}".format("PASS" if entry["ok"] else "FAIL", entry["name"], entry["detail"])
        )
    acceptance.write_line("logs and state: {0}".format(acceptance.root))
    if failure or failed:
        reasons = ["{0}/{1} assertions failed".format(len(failed), len(summary))]
        if failure:
            reasons.append(failure)
        acceptance.write_line("RESULT: FAIL ({0})".format("; ".join(reasons)))
        exit_code = 1
    else:
        acceptance.write_line("RESULT: PASS ({0}/{1} assertions)".format(len(summary), len(summary)))
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
