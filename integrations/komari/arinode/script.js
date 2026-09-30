const server = require("server");

function load() {
  server.route("GET", "/api/arinode/status", async (req, res) => {
    if (!req.context || req.context.role !== "admin") {
      res.statusCode = 403;
      res.end(JSON.stringify({ error: "admin access required" }));
      return;
    }

    const config = await server.getConfig();
    const endpoint = String(config.status_url || "http://127.0.0.1:18086/v1/status");
    if (!/^https?:\/\//.test(endpoint)) {
      res.statusCode = 400;
      res.end(JSON.stringify({ error: "status_url must use http(s)" }));
      return;
    }

    try {
      const headers = {};
      if (config.status_token) headers.Authorization = "Bearer " + String(config.status_token);
      const response = await fetch(endpoint, { headers });
      if (!response.ok) throw new Error("AriNode returned HTTP " + response.status);
      const status = await response.json();
      res.setHeader("Content-Type", "application/json");
      res.setHeader("Cache-Control", "no-store");
      res.end(JSON.stringify(status));
    } catch (error) {
      res.statusCode = 502;
      res.end(JSON.stringify({ error: String(error) }));
    }
  });
}
