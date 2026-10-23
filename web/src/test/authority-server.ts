import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { inject } from "vitest";

/** The real Go server; only the 48-hour timestamp advancement is a debug
 * trigger. Each process owns a separate temporary database and random port. */
export class AuthorityServer {
  private process: ChildProcessWithoutNullStreams;
  private lines: string[] = [];
  private waiters: ((line: string) => void)[] = [];
  private buffer = "";
  private error = "";
  private exited = false;
  url = "";
  private constructor(binary: string, open: boolean) {
    this.process = spawn(binary, open ? ["open"] : [], { stdio: "pipe" });
    this.process.stderr.setEncoding("utf8");
    this.process.stderr.on("data", (part: string) => { this.error += part; });
    this.process.once("exit", () => {
      this.exited = true;
      for (const waiter of this.waiters.splice(0)) waiter("fixture exited: " + this.error);
    });
    this.process.stdout.setEncoding("utf8");
    this.process.stdout.on("data", (part: string) => {
      this.buffer += part;
      for (;;) {
        const index = this.buffer.indexOf("\n");
        if (index < 0) break;
        const line = this.buffer.slice(0, index);
        this.buffer = this.buffer.slice(index + 1);
        const waiter = this.waiters.shift();
        if (waiter) waiter(line); else this.lines.push(line);
      }
    });
  }
  private line(): Promise<string> {
    if (this.exited) throw new Error(this.error);
    const line = this.lines.shift();
    return line === undefined ? new Promise((resolve) => this.waiters.push(resolve)) : Promise.resolve(line);
  }
  async ready(recoveryId: string) {
    this.process.stdin.write(JSON.stringify({ Op: "ready", RecoveryID: recoveryId }) + "\n");
    if (!JSON.parse(await this.line()).ok) throw new Error("fixture clock advancement failed");
  }
  async close() {
    if (this.process.exitCode !== null) return;
    await new Promise<void>((resolve) => {
      const timer = setTimeout(() => this.process.kill("SIGKILL"), 3000);
      this.process.once("exit", () => { clearTimeout(timer); resolve(); });
      this.process.stdin.end();
    });
  }
  static async start(open = false) {
    const server = new AuthorityServer(inject("authorityServerBinary"), open);
    server.url = await server.line();
    if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(server.url)) throw new Error("bad isolated server address: " + server.url);
    return server;
  }
}
