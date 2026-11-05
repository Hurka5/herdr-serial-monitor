// Everything that talks to serial-broker: the `serial` CLI for actions,
// the per-port log files for reading, watchFolder for wakeups.
import { exec, watchFolder } from "herdr";
import * as fs from "fs/promises";

const CLI_FALLBACK = "/.local/bin/serial";
const LOG_DIR_SUFFIX = "/.local/share/serial-broker/logs";

const LOG_LINE = /^(\d+) (\d+) ([RWF]) ?(.*)$/;

export const ENDINGS = [
  { id: "none", label: "No ending", suffix: "" },
  { id: "nl", label: "Newline", suffix: "\n" },
  { id: "cr", label: "Carriage return (CR)", suffix: "\r" },
  { id: "crlf", label: "NL & CR (CRLF)", suffix: "\r\n" },
];

export const BAUDS = [300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 28800, 38400,
  57600, 76800, 115200, 230400, 250000, 500000, 921600, 1000000];

export function parseLogLine(raw) {
  const match = LOG_LINE.exec(raw);
  if (match === null) {
    return null;
  }
  return { seq: +match[1], ts: +match[2], kind: match[3], text: match[4] };
}

export class Transport {
  constructor(homeDir) {
    this.home = homeDir;
    this.cliBin = localStorage.getItem("sm.cli") || "serial";
  }

  logsDir() {
    return this.home + LOG_DIR_SUFFIX;
  }

  logPath(portId) {
    return `${this.logsDir()}/${portId.replaceAll(":", "_")}.log`;
  }

  // The CLI is usually on PATH; some launch environments lack ~/.local/bin,
  // so fall back to the absolute path and remember what worked.
  async verifyCli() {
    if (await this._cliWorks(this.cliBin)) {
      return this.cliBin;
    }
    const fallback = this.home + CLI_FALLBACK;
    if (await this._cliWorks(fallback)) {
      this.cliBin = fallback;
      localStorage.setItem("sm.cli", fallback);
      return fallback;
    }
    throw new Error("serial CLI not found — is serial-broker installed?");
  }

  async _cliWorks(bin) {
    try {
      const result = await exec(bin, ["list"]);
      return result.code === 0;
    } catch {
      return false;
    }
  }

  async listPorts() {
    const result = await exec(this.cliBin, ["list"]);
    if (result.code !== 0) {
      throw new Error((result.stderr || result.stdout).trim());
    }
    return result.stdout.split("\n").slice(1).filter(Boolean).map(parsePortRow);
  }

  async send(portId, payload) {
    const result = await exec(this.cliBin, ["send", portId, payload, "--raw"]);
    if (result.code !== 0) {
      throw new Error((result.stderr || result.stdout).trim());
    }
  }

  async setBaud(portId, baud) {
    const result = await exec(this.cliBin, ["set", portId, "--baud", String(baud)]);
    if (result.code !== 0) {
      throw new Error((result.stderr || result.stdout).trim());
    }
  }

  async readEntries(portId, sinceSeq) {
    let text;
    try {
      text = await fs.readFile(this.logPath(portId), "utf8");
    } catch {
      return [];
    }
    const entries = [];
    for (const raw of text.split("\n")) {
      const entry = parseLogLine(raw);
      if (entry && entry.seq > sinceSeq) {
        entries.push(entry);
      }
    }
    return entries;
  }

  // Wakes `onActivity` whenever the broker writes anything into its logs.
  // A tight resolve loop (wakes under ~10ms apart) means the watch is
  // misbehaving: stop it and leave updates to the tick's fallback poll.
  async watchLogs(onActivity) {
    let lastWake = 0;
    let rapidWakes = 0;
    for (;;) {
      try {
        await watchFolder(this.logsDir());
      } catch {
        return;
      }
      const now = Date.now();
      rapidWakes = now - lastWake < 10 ? rapidWakes + 1 : 0;
      lastWake = now;
      if (rapidWakes > 5) {
        return;
      }
      onActivity();
    }
  }
}

function parsePortRow(line) {
  const [id, device, open, baud] = line.trim().split(/\s+/);
  return { id, device, open: open === "true", baud: +baud };
}
