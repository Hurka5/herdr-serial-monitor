import argparse
import logging
import signal
import sys
import threading

from .api import Client, Server, default_socket_path
from .broker import Broker
from .seriallog import default_data_dir

log = logging.getLogger("serial-broker")


def main(argv=None):
    parser = argparse.ArgumentParser(prog="serial-broker")
    parser.add_argument("--socket", default=default_socket_path())
    parser.add_argument("--data", default=str(default_data_dir()))
    parser.add_argument("--port", action="append", default=[], help="also own this device path")
    args = parser.parse_args(argv)

    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s",
                        datefmt="%Y-%m-%d %H:%M:%S")

    if Client(args.socket).alive():
        log.error("another serial-broker is already answering on %s — not starting", args.socket)
        return 1

    try:
        broker = Broker(data_dir=args.data, extra_ports=args.port, logf=log.info)
    except Exception as error:
        log.error("broker failed to start: %r", error)
        return 1
    try:
        server = Server(broker, args.socket)
    except OSError as error:
        log.error("listen %s: %s", args.socket, error)
        broker.close()
        return 1
    log.info("serial-broker listening on %s (logs in %s)", args.socket, args.data)

    stopped = threading.Event()
    for signal_number in (signal.SIGINT, signal.SIGTERM):
        signal.signal(signal_number, lambda *_: stopped.set())
    stopped.wait()

    log.info("shutting down")
    server.close()
    broker.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
