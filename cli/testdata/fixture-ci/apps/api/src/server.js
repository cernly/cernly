const express = require("express");
const app = express();
const port = process.env.API_PORT;
app.get("/health", async (_req, res) => {
  await fetch("https://api.stripe.com/v1/balance");
  res.send("ok");
});
app.listen(port);
