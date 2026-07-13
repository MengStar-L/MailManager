import { expect, test, type Page } from "@playwright/test";

async function openSettings(page: Page) {
  if (test.info().project.name === "mobile") {
    await page.getByRole("button", { name: "设置", exact: true }).click();
  } else {
    await page.getByRole("button", { name: "账户与设置" }).click();
  }
  await expect(page.getByRole("dialog", { name: "设置" })).toBeVisible();
}

async function openComposer(page: Page) {
  if (test.info().project.name === "mobile") await page.getByRole("button", { name: "写邮件", exact: true }).click();
  else await page.locator(".compose-button").click();
}

async function openDrafts(page: Page) {
  if (test.info().project.name === "mobile") {
    await page.getByRole("navigation", { name: "主要导航" }).getByRole("button", { name: "草稿", exact: true }).click();
  } else {
    await page.locator(".sidebar__nav .nav-item").filter({ hasText: "草稿" }).click();
  }
}

async function recoveryDraftCount(page: Page) {
  return page.evaluate(async () => {
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("mailmanager-draft-recovery");
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    try {
      return await new Promise<number>((resolve, reject) => {
        const request = database.transaction("drafts", "readonly").objectStore("drafts").count();
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
    } finally {
      database.close();
    }
  });
}

async function expectCustomSelectBounds(page: Page) {
  const listbox = page.getByRole("listbox");
  await expect(listbox).toBeVisible();
  const box = await listbox.boundingBox();
  const viewport = page.viewportSize();
  expect(box).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width);
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.y + box!.height).toBeLessThanOrEqual(viewport!.height);
  const style = await listbox.evaluate((element) => {
    const computed = getComputedStyle(element);
    return { background: computed.backgroundColor, borderRadius: computed.borderRadius, zIndex: computed.zIndex };
  });
  expect(style.background).toBe("rgb(255, 255, 255)");
  expect(style.borderRadius).toBe("6px");
  expect(Number(style.zIndex)).toBeGreaterThan(200);
  const itemHeight = await listbox.getByRole("option").first().evaluate((element) => element.getBoundingClientRect().height);
  expect(Math.abs(itemHeight - 34)).toBeLessThanOrEqual(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "统一收件箱" })).toBeVisible();
});

test("workspace is responsive and opens a conversation", async ({ page }) => {
  await expect(page).toHaveTitle("MailManager");
  await page.getByRole("button", { name: "打开 设计系统评审：最后一轮调整" }).first().click();
  await expect(page.locator(".reader__toolbar-title")).toContainText("设计系统评审：最后一轮调整");
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBe(0);
  await page.evaluate(() => localStorage.setItem("mailmanager-reduce-motion", "true"));
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-reduce-motion", "true");
  if (test.info().project.name === "mobile") {
    await page.emulateMedia({ reducedMotion: "reduce" });
    expect(await page.evaluate(() => matchMedia("(prefers-reduced-motion: reduce)").matches)).toBe(true);
  }
});

test("compose opens inside the reader pane without replacing the list", async ({ page }) => {
  await openComposer(page);
  const composer = page.getByRole("region", { name: "新邮件" });
  await expect(composer).toBeVisible();
  await expect(page.getByRole("dialog", { name: "新邮件" })).toHaveCount(0);
  await expect(page.locator(".dialog-overlay")).toHaveCount(0);
  await expect(page.locator(".mail-list__header h1")).toHaveText("统一收件箱");
  await expect(composer.getByRole("button", { name: "保存草稿" })).toBeVisible();
  const composerBox = await composer.boundingBox();
  const footerBox = await composer.locator(".composer__footer").boundingBox();
  expect(composerBox).not.toBeNull();
  expect(footerBox).not.toBeNull();
  expect(Math.abs(footerBox!.y + footerBox!.height - (composerBox!.y + composerBox!.height))).toBeLessThanOrEqual(1);
  if (test.info().project.name === "mobile") {
    expect(Math.abs(composerBox!.y - 56)).toBeLessThanOrEqual(1);
    expect(Math.abs(composerBox!.height - (page.viewportSize()!.height - 56))).toBeLessThanOrEqual(1);
  } else {
    const detailBox = await page.locator(".detail-slot").boundingBox();
    expect(detailBox).not.toBeNull();
    expect(Math.abs(composerBox!.x - detailBox!.x)).toBeLessThanOrEqual(1);
    expect(Math.abs(composerBox!.width - detailBox!.width)).toBeLessThanOrEqual(1);
    expect(Math.abs(composerBox!.height - detailBox!.height)).toBeLessThanOrEqual(1);
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
});

test("offline compose closes safely, restores from drafts, and syncs when online", async ({ page, context }) => {
  await openComposer(page);
  const composer = page.getByRole("region", { name: "新邮件" });
  await expect(composer).toBeVisible();
  await context.setOffline(true);
  expect(await page.evaluate(() => navigator.onLine)).toBe(false);
  await composer.getByLabel("主题").fill("离线恢复草稿");
  await composer.locator(".ProseMirror").fill("这段正文需要在误触关闭后恢复。");
  await composer.locator("input[type='file']").setInputFiles({ name: "offline-note.txt", mimeType: "text/plain", buffer: Buffer.from("offline attachment") });
  await composer.getByRole("button", { name: "关闭写信" }).click();
  await expect(composer).toHaveCount(0);
  expect(await recoveryDraftCount(page)).toBe(1);

  await openDrafts(page);
  const localDraft = page.locator(".draft-row").filter({ hasText: "离线恢复草稿" });
  await expect(localDraft.getByText("本地待同步")).toBeVisible();
  await localDraft.click();

  const recovered = page.getByRole("region", { name: "编辑草稿" });
  await expect(recovered.getByLabel("主题")).toHaveValue("离线恢复草稿");
  await expect(recovered.locator(".ProseMirror")).toContainText("这段正文需要在误触关闭后恢复");
  await expect(recovered.getByText("offline-note.txt", { exact: true })).toBeVisible();
  await recovered.getByRole("button", { name: "关闭写信" }).click();
  await expect(recovered).toHaveCount(0);
  await expect(localDraft.getByText("本地待同步")).toBeVisible();
  await context.setOffline(false);
  await expect(page.locator(".draft-row").filter({ hasText: "离线恢复草稿" }).getByText("草稿", { exact: true })).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText("本地待同步")).toHaveCount(0);
});

test("custom selects stay styled, keyboard accessible, and inside the viewport", async ({ page }) => {
  await openComposer(page);
  const sender = page.getByRole("combobox", { name: "发件人" });
  await sender.focus();
  await page.keyboard.press("ArrowDown");
  await expectCustomSelectBounds(page);
  await page.getByRole("option", { name: "个人 · lin@example.com" }).press("Enter");
  await expect(sender).toContainText("个人 · lin@example.com");
  await page.getByRole("button", { name: "关闭写信" }).click();

  await openSettings(page);
  await page.getByRole("button", { name: "编辑连接 70425@qq.com" }).click();
  const connectionEditor = page.locator(".account-connection-editor");
  await expect(connectionEditor).toHaveCSS("opacity", "1");
  await expect(connectionEditor).toHaveCSS("transform", "none");
  const tls = page.getByRole("combobox", { name: "IMAP TLS" });
  await tls.focus();
  await page.keyboard.press("ArrowDown");
  await expectCustomSelectBounds(page);
  await page.getByRole("option", { name: "STARTTLS" }).press("Enter");
  await expect(tls).toContainText("STARTTLS");
});

test("lazy compose failure keeps the workspace visible", async ({ page }) => {
  await page.route("**/src/components/Composer.tsx*", (route) => route.abort());
  await openComposer(page);
  await expect(page.getByRole("alert")).toContainText("写信页面加载失败，工作台仍可使用");
  await expect(page.getByRole("heading", { name: "统一收件箱" })).toBeVisible();
});

test("conversation transitions animate content without remounting persistent controls", async ({ page }) => {
  const firstConversation = page.getByRole("button", { name: "打开 设计系统评审：最后一轮调整", exact: true }).first();
  await firstConversation.click();
  await expect(page.locator(".reader__toolbar-title")).toContainText("设计系统评审：最后一轮调整");
  await expect(page.locator(".conversation-row__selection")).toHaveCount(1);

  const toolbarEnd = page.locator(".reader__toolbar-end");
  await toolbarEnd.evaluate((element) => element.setAttribute("data-transition-instance", "retained"));
  const toolbarBefore = await toolbarEnd.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, right: rect.right, animationName: getComputedStyle(element).animationName };
  });
  expect(toolbarBefore.animationName).toBe("none");

  if (test.info().project.name !== "mobile") {
    const targetButton = page.getByRole("button", { name: "打开 下周项目节奏与交付节点", exact: true });
    const targetRow = targetButton.locator("..");
    await page.evaluate(() => {
      const stage = document.querySelector(".reader__stage");
      if (!stage) return;
      document.documentElement.dataset.readerLayerPeak = String(stage.querySelectorAll(".reader__scroll").length);
      const observer = new MutationObserver(() => {
        const count = stage.querySelectorAll(".reader__scroll").length;
        document.documentElement.dataset.readerLayerPeak = String(Math.max(count, Number(document.documentElement.dataset.readerLayerPeak ?? 0)));
      });
      observer.observe(stage, { childList: true });
      window.setTimeout(() => observer.disconnect(), 2_000);
    });
    await targetButton.click();
    await expect(page.locator(".reader__toolbar-title")).toContainText("下周项目节奏与交付节点");
    await expect(toolbarEnd).toHaveAttribute("data-transition-instance", "retained");
    await expect(page.locator(".reader__stage .reader__scroll")).toHaveCount(1, { timeout: 1_000 });
    await expect(page.locator(".reader__toolbar-title-content")).toHaveCount(1, { timeout: 1_000 });
    await expect(page.locator("html")).toHaveAttribute("data-reader-layer-peak", "2");
    const selectionBox = await page.locator(".conversation-row__selection").boundingBox();
    const targetBox = await targetRow.boundingBox();
    expect(selectionBox).not.toBeNull();
    expect(targetBox).not.toBeNull();
    expect(Math.abs(selectionBox!.y - targetBox!.y)).toBeLessThanOrEqual(1);
    expect(Math.abs(selectionBox!.height - targetBox!.height)).toBeLessThanOrEqual(1);
  }

  const toolbarAfter = await toolbarEnd.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return { top: rect.top, right: rect.right, animationName: style.animationName, opacity: style.opacity };
  });
  expect(Math.abs(toolbarAfter.top - toolbarBefore.top)).toBeLessThanOrEqual(1);
  expect(Math.abs(toolbarAfter.right - toolbarBefore.right)).toBeLessThanOrEqual(1);
  expect(toolbarAfter.animationName).toBe("none");
  expect(toolbarAfter.opacity).toBe("1");
  expect(await page.locator(".reader__toolbar-title").evaluate((element) => getComputedStyle(element).animationName)).toBe("none");
  expect(await page.locator(".message-body-frame").evaluate((element) => getComputedStyle(element).transform)).toBe("none");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
});

test("historical messages expand smoothly without closing the current message", async ({ page }) => {
  await page.getByRole("button", { name: "打开 设计系统评审：最后一轮调整", exact: true }).first().click();
  await expect(page.locator(".message-card")).toHaveCount(3);
  const cards = page.locator(".message-card");
  const firstCard = cards.first();
  const expectedCollapsedHeight = test.info().project.name === "mobile" ? 68 : 72;
  expect(Math.abs((await firstCard.boundingBox())!.height - expectedCollapsedHeight)).toBeLessThanOrEqual(1);

  await firstCard.locator(".message-card__header").click();
  await expect(firstCard).toHaveClass(/is-open/);
  await expect(cards.locator(".message-card__actions")).toHaveCount(2);
  await page.waitForTimeout(240);
  expect((await firstCard.boundingBox())!.height).toBeGreaterThan(300);

  await firstCard.locator(".message-card__header").click();
  await expect(firstCard).not.toHaveClass(/is-open/);
  await page.waitForTimeout(240);
  expect(Math.abs((await firstCard.boundingBox())!.height - expectedCollapsedHeight)).toBeLessThanOrEqual(1);
  await expect(cards.locator(".message-card__actions")).toHaveCount(1);
});

test("virtual conversation rows stay fixed and do not animate while scrolling", async ({ page }) => {
  const scroll = page.locator(".mail-list__scroll");
  const rows = page.locator(".virtual-row");
  const expectedHeight = test.info().project.name === "mobile" ? 108 : 113;

  if (test.info().project.name === "desktop") {
    await expect(rows.locator(".conversation-row").first()).toBeVisible();
  }
  await page.waitForTimeout(220);
  await expect(page.locator(".virtual-row--enter")).toHaveCount(0);

  const initialIndex = Number(await rows.first().getAttribute("data-index"));
  await scroll.hover();
  for (let index = 0; index < 12; index += 1) {
    await page.mouse.wheel(0, 260);
    await page.waitForTimeout(16);
  }
  await expect.poll(async () => Number(await rows.first().getAttribute("data-index"))).toBeGreaterThan(initialIndex);

  const metrics = await rows.evaluateAll((elements) => elements.map((element) => {
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return { height: rect.height, inlineOpacity: (element as HTMLElement).style.opacity, animationName: style.animationName, top: rect.top, bottom: rect.bottom };
  }));
  expect(metrics.length).toBeGreaterThan(0);
  for (const metric of metrics) {
    expect(Math.abs(metric.height - expectedHeight)).toBeLessThanOrEqual(1);
    expect(metric.inlineOpacity).toBe("");
    expect(metric.animationName).toBe("none");
  }
  for (let index = 1; index < metrics.length; index += 1) {
    expect(Math.abs(metrics[index].top - metrics[index - 1].bottom)).toBeLessThanOrEqual(1);
  }
  expect(await scroll.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
});

test("checked conversation rows keep centered visible checkmarks", async ({ page }) => {
  await page.getByRole("checkbox", { name: "选择当前列表" }).check();
  await expect(page.getByRole("toolbar", { name: "批量操作" })).toBeVisible();

  const rows = page.locator(".virtual-row .conversation-row");
  await expect(rows.first()).toHaveClass(/is-checked/);
  const centerOffset = await rows.first().locator(".checkbox").evaluate((element) => {
    const root = element.getBoundingClientRect();
    const icon = element.querySelector("svg")!.getBoundingClientRect();
    return {
      x: Math.abs((root.left + root.width / 2) - (icon.left + icon.width / 2)),
      y: Math.abs((root.top + root.height / 2) - (icon.top + icon.height / 2)),
    };
  });
  expect(centerOffset.x).toBeLessThanOrEqual(1);
  expect(centerOffset.y).toBeLessThanOrEqual(1);

  const scroll = page.locator(".mail-list__scroll");
  await scroll.evaluate((element) => { element.scrollTop = element.scrollHeight * 0.65; });
  await expect.poll(async () => Number(await page.locator(".virtual-row").first().getAttribute("data-index"))).toBeGreaterThan(0);
  const checkedRows = page.locator(".virtual-row .conversation-row.is-checked");
  await expect(checkedRows).toHaveCount(await rows.count());
  const opacities = await checkedRows.locator(".conversation-row__select").evaluateAll((elements) => elements.map((element) => getComputedStyle(element).opacity));
  expect(opacities.every((opacity) => opacity === "1")).toBe(true);

  const firstVisibleCheckbox = rows.first().getByRole("checkbox");
  const checkboxName = await firstVisibleCheckbox.getAttribute("aria-label");
  const targetCheckbox = page.getByRole("checkbox", { name: checkboxName!, exact: true });
  const targetRow = targetCheckbox.locator("xpath=ancestor::article");
  await targetCheckbox.uncheck();
  await page.keyboard.press("Tab");
  const header = await page.locator(".mail-list__header").boundingBox();
  await page.mouse.move(header!.x + 8, header!.y + 8);
  await expect(targetRow).not.toHaveClass(/is-checked/);
  await expect(targetRow.locator(".conversation-row__select")).toHaveCSS("opacity", "0");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
});

test("compact density remeasures the virtual list without gaps", async ({ page }) => {
  await page.evaluate(() => localStorage.setItem("mailmanager-density", "compact"));
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-density", "compact");
  await page.waitForTimeout(220);

  const scroll = page.locator(".mail-list__scroll");
  await scroll.hover();
  await page.mouse.wheel(0, 1_800);
  await expect.poll(async () => Number(await page.locator(".virtual-row").first().getAttribute("data-index"))).toBeGreaterThan(0);
  const metrics = await page.locator(".virtual-row").evaluateAll((elements) => elements.map((element) => {
    const rect = element.getBoundingClientRect();
    return { height: rect.height, top: rect.top, bottom: rect.bottom, animationName: getComputedStyle(element).animationName };
  }));
  for (const metric of metrics) {
    expect(Math.abs(metric.height - 93)).toBeLessThanOrEqual(1);
    expect(metric.animationName).toBe("none");
  }
  for (let index = 1; index < metrics.length; index += 1) {
    expect(Math.abs(metrics[index].top - metrics[index - 1].bottom)).toBeLessThanOrEqual(1);
  }
});

test("fills the reader viewport while keeping scaling and message actions usable", async ({ page }) => {
  await page.locator(".conversation-row__open").first().click();
  const card = page.locator(".message-card.is-open");
  const viewport = page.locator(".reader__scroll");
  const toolbar = page.locator(".reader__toolbar");
  const header = card.locator(".message-card__header");
  const actions = card.locator(".message-card__actions");
  const body = card.locator(".message-card__body");
  const messageBody = card.locator(".message-body");
  const frame = card.locator(".message-body-frame");
  const scale = page.getByLabel("邮件正文缩放");

  await expect(actions).toBeVisible();
  await expect(frame).toBeVisible();
  await expect(scale).toHaveText("85%");
  const selectedText = await page.frameLocator(".message-body-frame").locator("body").evaluate((element) => {
    const selection = element.ownerDocument.getSelection();
    const range = element.ownerDocument.createRange();
    range.selectNodeContents(element);
    selection?.removeAllRanges();
    selection?.addRange(range);
    return selection?.toString() ?? "";
  });
  expect(selectedText.length).toBeGreaterThan(0);
  await frame.evaluate((element) => {
    element.setAttribute("data-zoom-instance", "retained");
    element.setAttribute("data-zoom-loads", "0");
    element.setAttribute("data-zoom-srcdoc", element.getAttribute("srcdoc") ?? "");
    element.addEventListener("load", () => element.setAttribute("data-zoom-loads", String(Number(element.getAttribute("data-zoom-loads")) + 1)));
  });
  const bodyScrollBeforeZoom = await body.evaluate((element) => {
    element.scrollTop = Math.min(20, element.scrollHeight - element.clientHeight);
    return element.scrollTop;
  });
  await page.getByRole("button", { name: "放大邮件正文" }).click();
  await expect(scale).toHaveText("90%");
  await expect(frame).toHaveAttribute("data-zoom-instance", "retained");
  await expect(frame).toHaveAttribute("data-zoom-loads", "0");
  expect(await frame.evaluate((element) => element.getAttribute("srcdoc"))).toBe(await frame.getAttribute("data-zoom-srcdoc"));
  expect(await body.evaluate((element) => element.scrollTop)).toBe(bodyScrollBeforeZoom);
  expect(await page.evaluate(() => localStorage.getItem("mailmanager-body-scale"))).toBe("90");

  const messageBodyBox = await messageBody.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, left: rect.left, width: rect.width, height: rect.height };
  });
  const frameBox = await frame.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, left: rect.left, width: rect.width, height: rect.height, zoom: Number(getComputedStyle(element).zoom) };
  });
  expect(frameBox.zoom).toBe(0.9);
  expect(Math.abs(messageBodyBox.top - frameBox.top)).toBeLessThanOrEqual(1);
  expect(Math.abs(messageBodyBox.left - frameBox.left)).toBeLessThanOrEqual(1);
  expect(Math.abs(messageBodyBox.width - frameBox.width * frameBox.zoom)).toBeLessThanOrEqual(1);
  expect(Math.abs(messageBodyBox.height - frameBox.height * frameBox.zoom)).toBeLessThanOrEqual(1);
  expect(await toolbar.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
  await expect.poll(async () => {
    const [cardBox, viewportBox] = await Promise.all([card, viewport].map((locator) => locator.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom, height: rect.height };
    })));
    return {
      topGap: Math.round(cardBox.top - viewportBox.top),
      bottomGap: Math.round(viewportBox.bottom - cardBox.bottom),
    };
  }).toEqual({ topGap: 16, bottomGap: 16 });

  const [cardBox, viewportBox, headerBox, actionsBox] = await Promise.all([card, viewport, header, actions].map((locator) => locator.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, bottom: rect.bottom };
  })));
  expect(actionsBox.bottom).toBeLessThanOrEqual(viewportBox.bottom + 1);
  expect(actionsBox.bottom).toBeLessThan(cardBox.bottom);
  await expect(body).toHaveCSS("overflow-y", "auto");
  const bodyScroll = await body.evaluate((element) => ({ clientHeight: element.clientHeight, scrollHeight: element.scrollHeight }));
  expect(bodyScroll.scrollHeight).toBeGreaterThan(bodyScroll.clientHeight);
  await body.evaluate((element) => { element.scrollTop = element.scrollHeight; });
  const [headerAfterScroll, actionsAfterScroll] = await Promise.all([header, actions].map((locator) => locator.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, bottom: rect.bottom };
  })));
  expect(Math.abs(headerAfterScroll.top - headerBox.top)).toBeLessThanOrEqual(1);
  expect(Math.abs(actionsAfterScroll.bottom - actionsBox.bottom)).toBeLessThanOrEqual(1);

  const viewportScroll = await viewport.evaluate((element) => ({ clientHeight: element.clientHeight, scrollHeight: element.scrollHeight }));
  expect(viewportScroll.scrollHeight).toBeGreaterThan(viewportScroll.clientHeight);
});

test("single-message reader stays fixed while its body remains scrollable", async ({ page }) => {
  await page.getByRole("button", { name: "打开 下周项目节奏与交付节点", exact: true }).click();
  const viewport = page.locator(".reader__scroll");
  const card = page.locator(".message-card.is-open");
  const body = card.locator(".message-card__body");
  await expect(viewport).toHaveClass(/reader__scroll--single/);
  await expect(card).toBeVisible();

  const before = await card.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, bottom: rect.bottom };
  });
  await viewport.evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await viewport.hover();
  await page.mouse.wheel(0, 600);
  await page.waitForTimeout(50);
  expect(await viewport.evaluate((element) => element.scrollTop)).toBe(0);

  const after = await card.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, bottom: rect.bottom };
  });
  expect(Math.abs(after.top - before.top)).toBeLessThanOrEqual(1);
  expect(Math.abs(after.bottom - before.bottom)).toBeLessThanOrEqual(1);

  await expect(body).toHaveCSS("overflow-y", "auto");
  await expect(body.locator(".message-body-frame")).toBeVisible();
});

test("reply excludes original attachments while forward includes them", async ({ page }) => {
  await page.getByRole("button", { name: "打开 设计系统评审：最后一轮调整" }).first().click();
  await page.getByRole("button", { name: "回复", exact: true }).first().click();
  const reply = page.getByRole("region", { name: "回复邮件" });
  await expect(reply).toBeVisible();
  await expect(reply.getByText("项目评审稿.pdf", { exact: true })).toHaveCount(0);
  await expect(reply.locator(".ProseMirror")).toContainText("留白和层级问题");
  await reply.getByRole("button", { name: "关闭写信" }).click();
  await expect(page.locator(".reader__toolbar-title")).toContainText("设计系统评审：最后一轮调整");

  await page.getByRole("button", { name: "转发", exact: true }).first().click();
  const forward = page.getByRole("region", { name: "转发邮件" });
  await expect(forward).toBeVisible();
  await expect(forward.getByText("项目评审稿.pdf", { exact: true })).toBeVisible();
  await expect(forward.locator(".ProseMirror")).toContainText("留白和层级问题");
});

test("search, bulk archive and undo remain usable", async ({ page }) => {
  const search = page.getByRole("textbox", { name: "搜索邮件" });
  await search.fill("Dependency update");
  await expect(page.getByText("[MailManager] Dependency update summary", { exact: true }).first()).toBeVisible();
  await page.getByRole("checkbox", { name: "选择 [MailManager] Dependency update summary" }).first().check();
  await page.getByRole("toolbar", { name: "批量操作" }).getByRole("button", { name: "归档" }).click();
  const notice = page.getByRole("status");
  await expect(notice).toContainText("会话已归档");
  await notice.getByRole("button", { name: "撤销" }).click();
  await expect(page.getByText("[MailManager] Dependency update summary", { exact: true }).first()).toBeVisible();
});

test("account settings support password reconnect, sync and OAuth reauthorization", async ({ page }) => {
  await openSettings(page);
  const dialog = page.getByRole("dialog", { name: "设置" });
  const qq = dialog.locator(".settings-account").filter({ hasText: "70425@qq.com" });
  await qq.getByRole("button", { name: "编辑连接 70425@qq.com" }).click();
  const smtp = qq.getByRole("group", { name: "SMTP" });
  await expect(smtp.getByLabel("端口")).toHaveValue("465");
  await qq.getByRole("button", { name: "保存连接" }).click();
  await expect(qq).toContainText("连接已保存，正在同步");
  await qq.getByRole("button", { name: "立即同步" }).click();
  await expect(qq).toContainText("已开始同步");

  const gmail = dialog.locator(".settings-account").filter({ hasText: "lin@atlas.studio" });
  await gmail.getByRole("button", { name: "重新授权 lin@atlas.studio" }).click();
  await expect(page).toHaveURL(/\?oauth_demo=complete$/);
});

test("system update notice installs and reconnects without overflowing", async ({ page }) => {
  await openSettings(page);
  const dialog = page.getByRole("dialog", { name: "设置" });
  const updateTab = dialog.getByRole("tab", { name: "系统更新" });
  await updateTab.click();
  await expect(updateTab).toHaveAttribute("data-state", "active");
  await expect(dialog.getByText("发现新版本 v1.1.0")).toBeVisible();

  const tabListBox = await dialog.getByRole("tablist", { name: "设置分类" }).boundingBox();
  const updateTabBox = await updateTab.boundingBox();
  expect(tabListBox).not.toBeNull();
  expect(updateTabBox).not.toBeNull();
  expect(updateTabBox!.x).toBeGreaterThanOrEqual(tabListBox!.x);
  expect(updateTabBox!.x + updateTabBox!.width).toBeLessThanOrEqual(tabListBox!.x + tabListBox!.width + 1);

  const installButton = dialog.locator(".update-actions > button");
  await expect(installButton).toHaveAccessibleName("更新并重启到 v1.1.0");
  await installButton.click();
  const restart = page.locator(".update-restart-panel");
  await expect(restart).toBeVisible();
  await expect(restart).toContainText(/等待更新器|正在下载|正在安装|正在重启/);
  await expect(installButton).toBeDisabled();

  await expect(restart).toHaveCount(0, { timeout: 8_000 });
  await expect(dialog.getByText("更新完成").first()).toBeVisible();
  await expect(dialog.getByText("v1.1.0").first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);

  if (test.info().project.name === "mobile") {
    const tabStyle = await updateTab.evaluate((element) => {
      const style = getComputedStyle(element);
      return { overflow: element.scrollWidth - element.clientWidth, animationDuration: style.animationDuration };
    });
    expect(tabStyle.overflow).toBeLessThanOrEqual(0);
  }

  await page.screenshot({ path: `../output/playwright/system-update-${test.info().project.name}.png` });
});

test("logout completes a full page reload", async ({ page }) => {
  if (test.info().project.name === "mobile") {
    await page.getByRole("button", { name: "打开导航" }).click();
  }
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect.poll(async () => {
    try {
      return await page.evaluate(() => (performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming | undefined)?.type);
    } catch {
      return undefined;
    }
  }).toBe("reload");
  await expect(page.getByRole("heading", { name: "统一收件箱" })).toBeVisible();
});
