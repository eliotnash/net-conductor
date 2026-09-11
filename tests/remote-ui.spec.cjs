const { test, expect } = require("@playwright/test");
const path = require("node:path");
test.use({
  launchOptions: { executablePath: process.env.NC_TEST_BROWSER },
  viewport: { width: 1000, height: 800 },
});
async function setup(page) {
  const calls = [];
  await page.route("http://127.0.0.1:19876/", (r) =>
    r.fulfill({
      contentType: "text/html",
      body: "<style>.remote-screen{width:400px;height:300px} dialog{width:700px}</style>",
    }),
  );
  await page.routeWebSocket("**/api/remote/**", (ws) =>
    ws.onMessage((raw) => {
      const m = JSON.parse(raw);
      calls.push(m);
      if (m.type === "relay") {
        const r = m.data;
        let result = null;
        if (r.op === "list") result = { path: "test", entries: [] };
        if (r.op === "frame")
          result = {
            image:
              "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jP1cAAAAASUVORK5CYII=",
            width: 1,
            height: 1,
          };
        ws.send(
          JSON.stringify({
            type: "relay",
            data: { type: "result", id: r.id, ok: true, result },
          }),
        );
      }
    }),
  );
  await page.goto("http://127.0.0.1:19876/");
  await page.addScriptTag({ path: path.resolve("web/static/remote-ui.js") });
  return calls;
}
test("relay-only file session does not wait for ICE and reports actual path", async ({
  page,
}) => {
  const calls = await setup(page);
  await page.evaluate(() =>
    window.openRemote("test", "files", "test", "relay"),
  );
  await expect(page.locator(".remote-status")).toHaveText(
    "服务器中转 · HTTPS/WebSocket",
  );
  expect(calls.find((c) => c.type === "offer").data.transport).toBe("relay");
  expect(
    calls.some((c) => c.type === "relay" && c.data.op === "list"),
  ).toBeTruthy();
  await page.getByRole("button", { name: "关闭", exact: true }).click();
});
test("desktop starts view-only and click includes coordinates; keyboard is separate", async ({
  page,
}) => {
  const calls = await setup(page);
  await page.evaluate(() => {
    window.openRemote("test", "desktop", "test", "relay");
  });
  const img = page.locator(".remote-screen");
  await expect(img).toBeVisible();
  await img.click({ position: { x: 120, y: 90 } });
  expect(
    calls.some(
      (c) =>
        c.type === "relay" &&
        c.data.op === "input" &&
        c.data.input.kind === "button",
    ),
  ).toBeFalsy();
  await page.locator(".remote-control").check();
  await img.click({ position: { x: 120, y: 90 } });
  await expect
    .poll(
      () =>
        calls.filter(
          (c) => c.type === "relay" && c.data.input?.kind === "button",
        ).length,
    )
    .toBe(2);
  const down = calls.find(
    (c) => c.data?.input?.kind === "button" && c.data.input.down,
  ).data.input;
  expect(down.x).toBeCloseTo(0.3);
  expect(down.y).toBeCloseTo(0.3);
  await img.press("a");
  await expect
    .poll(() => calls.filter((c) => c.data?.input?.kind === "key").length)
    .toBe(2);
  await page.getByRole("button", { name: "关闭", exact: true }).click();
});
