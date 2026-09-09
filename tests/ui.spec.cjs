const { test, expect } = require("@playwright/test");
test.use({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } });
test("admin interface loads real state and opens configuration dialogs", async ({
  page,
}) => {
  test.skip(
    !process.env.NC_TEST_URL || !process.env.NC_TEST_PASSWORD,
    "Set NC_TEST_URL and NC_TEST_PASSWORD for an isolated test server.",
  );
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(process.env.NC_TEST_URL);
  await page.getByLabel("管理密码").fill(process.env.NC_TEST_PASSWORD);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { name: "私网拓扑" })).toBeVisible();
  await page.getByRole("button", { name: "组网设备", exact: true }).click();
  await expect(page.getByRole("heading", { name: "全部设备" })).toBeVisible();
  await page.getByRole("button", { name: "端口映射", exact: true }).click();
  await page.getByRole("button", { name: "＋ 添加映射", exact: true }).click();
  await expect(page.getByLabel("映射名称")).toBeVisible();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  expect(errors).toEqual([]);
});
