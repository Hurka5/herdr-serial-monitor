import json
import os
import socket
import socketserver
import threading

MAX_REQUEST = 1 << 20
EMPTY_LINES = []


def default_socket_path():
    runtime_dir = os.environ.get("XDG_RUNTIME_DIR") or "/tmp"
    return os.path.join(runtime_dir, "serial-broker.sock")


class ApiError(Exception):
    pass


def handle_request(broker, request):
    method = request.get("method")
    if method == "list":
        return broker.list()
    if method == "read":
        lines, next_cursor = broker.read(request.get("port", ""), int(request.get("since", 0)))
        return {"lines": lines or EMPTY_LINES, "nextCursor": next_cursor}
    if method == "write":
        return {"written": broker.write(request.get("port", ""), request.get("data", ""))}
    if method == "flash":
        return broker.flash(request.get("port", ""), request.get("argv") or [])
    if method == "free":
        broker.free(request.get("port", ""))
        return {"ok": True}
    if method == "set":
        broker.set(request.get("port", ""), int(request.get("baud", 0)))
        return {"ok": True}
    raise ApiError(f'unknown method "{method}"')


def _handler_class(broker):
    class Handler(socketserver.StreamRequestHandler):
        def handle(self):
            line = self.rfile.readline(MAX_REQUEST)
            try:
                response = handle_request(broker, json.loads(line))
            except (ApiError, RuntimeError) as error:
                response = {"error": str(error)}
            except (ValueError, KeyError) as error:
                response = {"error": f"bad request: {error}"}
            except Exception as error:
                response = {"error": f"internal error: {error!r}"}
            self.wfile.write(json.dumps(response).encode() + b"\n")

    return Handler


class Server:
    """Serves the broker over a unix socket: one JSON request per connection."""

    def __init__(self, broker, path):
        self.broker = broker
        self.path = path

        try:
            os.unlink(path)
        except FileNotFoundError:
            pass
        self.server = socketserver.ThreadingUnixStreamServer(path, _handler_class(broker))
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        try:
            os.unlink(self.path)
        except FileNotFoundError:
            pass


class Client:
    def __init__(self, socket_path=None, timeout=10.0):
        self.socket_path = socket_path or default_socket_path()
        self.timeout = timeout

    def request(self, payload):
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
            sock.settimeout(self.timeout)
            try:
                sock.connect(self.socket_path)
                sock.sendall(json.dumps(payload).encode() + b"\n")
                chunks = []
                while True:
                    chunk = sock.recv(65536)
                    if not chunk:
                        break
                    chunks.append(chunk)
                    if b"\n" in chunk:
                        break
            except OSError as error:
                raise ApiError(f"serial-broker unreachable: {error}") from error
        try:
            response = json.loads(b"".join(chunks).decode())
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise ApiError(f"serial-broker sent no usable answer: {error}") from error
        if isinstance(response, dict) and "error" in response:
            raise ApiError(response["error"])
        return response

    def alive(self):
        try:
            self.request({"method": "list"})
            return True
        except (ApiError, OSError):
            return False

    def list(self):
        return self.request({"method": "list"})

    def read(self, port, since=0):
        return self.request({"method": "read", "port": port, "since": since})

    def write(self, port, data):
        return self.request({"method": "write", "port": port, "data": data})

    def flash(self, port, argv):
        return self.request({"method": "flash", "port": port, "argv": argv})

    def free(self, port):
        return self.request({"method": "free", "port": port})

    def set(self, port, baud):
        return self.request({"method": "set", "port": port, "baud": baud})
