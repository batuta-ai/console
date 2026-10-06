/// <reference types="node" />
import { createServer } from "node:http";
import { expect, test } from "vitest";
import { api, journeyMutation } from "./lib";

for (const status of [307, 308]) {
  for (const method of ["journey", "POST", "PUT"]) {
    test(`${method} does not replay its body through HTTP ${status}`, async () => {
      const requests: { path: string; method: string; body: string }[] = [];
      const server = createServer((request, response) => {
        let body = "";
        request.setEncoding("utf8");
        request.on("data", chunk => { body += chunk; });
        request.on("end", () => {
          requests.push({ path: request.url || "", method: request.method || "", body });
          if (request.url === "/mutate") {
            response.writeHead(status, { Location: "/replayed" });
            response.end();
          } else {
            response.writeHead(200, { "Content-Type": "application/json" });
            response.end(JSON.stringify({ effect: "recorded" }));
          }
        });
      });
      await new Promise<void>((resolve, reject) => {
        server.once("error", reject);
        server.listen(0, "127.0.0.1", resolve);
      });
      try {
        const address = server.address();
        if (!address || typeof address === "string") throw Error("Missing owned loopback address");
        const url = `http://127.0.0.1:${address.port}/mutate`;
        const body = { mission_id: "synthetic-redirect-proof", version: "exact-version" };
        const result = method === "journey" ? journeyMutation(url, body) : api(url, { method, body: JSON.stringify(body), redirect: "follow" });
        await expect(result).rejects.toThrow();
        expect(requests).toEqual([{ path: "/mutate", method: method === "journey" ? "POST" : method, body: JSON.stringify(body) }]);
      } finally {
        await new Promise<void>(resolve => server.close(() => resolve()));
      }
    });
  }
}
